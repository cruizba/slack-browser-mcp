package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/archive"
	"github.com/cruizba/slack-browser-mcp/internal/config"
	"github.com/cruizba/slack-browser-mcp/internal/forum"
)

// forumCatchUp is the budget a forum read spends refreshing the bound channels: a read
// has to see what the other side said, and waiting forever for it would make a typo in
// a channel name look like a hang.
const forumCatchUp = 10 * time.Second

// ForumCatalog reads the agent-forum surface: the instructions an agent gets once and
// the tools it may call.
func ForumCatalog() (string, []Tool, error) {
	return catalogFrom(forumCatalogue())
}

// forumCatalogue is the embedded file, kept apart so the slack surface's catalogue is
// the one `serve` reads.
func forumCatalogue() []byte {
	raw, err := toolFiles.ReadFile("forum_tools.json")
	if err != nil {
		return nil
	}

	return raw
}

// catalogFrom is the reader both surfaces share: pretty-printed on disk, compact on
// the wire.
func catalogFrom(raw []byte) (string, []Tool, error) {
	if len(raw) == 0 {
		return "", nil, errors.New("the tool catalogue is missing")
	}

	var parsed catalog
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", nil, err
	}

	tools := make([]Tool, 0, len(parsed.Tools))

	for _, schema := range parsed.Tools {
		var (
			name    struct{ Name string }
			compact bytes.Buffer
		)

		if err := json.Unmarshal(schema, &name); err != nil {
			return "", nil, err
		}

		if err := json.Compact(&compact, schema); err != nil {
			return "", nil, err
		}

		tools = append(tools, Tool{Name: name.Name, Schema: compact.Bytes()})
	}

	return parsed.Instructions, tools, nil
}

// ForumHandlers is the agent-forum surface.
func ForumHandlers(deps Deps) map[string]Handler {
	return map[string]Handler{
		"forum_status":    forumStatusTool(deps),
		"forum_inbox":     forumInboxTool(deps),
		"forum_task":      forumTaskTool(deps),
		"forum_peers":     forumPeersTool(deps),
		"forum_post":      forumPostTool(deps),
		"forum_accept":    forumMoveTool(deps, forum.KindAccept),
		"forum_decline":   forumMoveTool(deps, forum.KindDecline),
		"forum_progress":  forumMoveTool(deps, forum.KindProgress),
		"forum_result":    forumMoveTool(deps, forum.KindResult),
		"forum_ack":       forumMoveTool(deps, forum.KindAck),
		"forum_reopen":    forumMoveTool(deps, forum.KindReopen),
		"forum_cancel":    forumMoveTool(deps, forum.KindCancel),
		"forum_say":       forumSayTool(deps),
		"archive_refresh": archiveRefreshTool(deps),
	}
}

// openForumContext assembles the forum state the tools read and write.
func openForumContext(deps Deps) (forum.Context, error) {
	return forum.OpenForum(forum.OpenOptions{
		Now:       time.Now(),
		Store:     forum.NewStore(deps.Paths.ForumFile),
		Archive:   archive.NewStore(deps.Paths.ArchiveDir),
		Decisions: forum.NewDecisionLog(deps.Paths.ForumDir),
		Env:       deps.Env,
		AuditLog:  deps.Paths.AuditLog,
	})
}

// forumTransports builds one transport per workspace, from the stored session.
func forumTransports(deps Deps) forum.TransportResolver {
	return func(teamID string) (forum.Transport, error) {
		account, err := newAuthStore(deps).Get(teamID)
		if err != nil {
			return forum.Transport{}, err
		}

		settings, err := config.NewStore(deps.Paths.SettingsFile).Read()
		if err != nil {
			return forum.Transport{}, err
		}

		signing, err := config.SigningFor(settings, deps.Env)
		if err != nil {
			return forum.Transport{}, err
		}

		return forum.Transport{Account: account, Client: newClient(deps, account), Signing: signing}, nil
	}
}

// refresh catches the bound channels up, which is what makes a write decide against the
// thread instead of against whatever this machine last saw.
func refresh(deps Deps, forumContext forum.Context, deadline time.Duration) forum.CatchUpReport {
	return forum.CatchUp(forum.CatchUpArgs{
		Bindings: forumContext.File.Channels,
		Deadline: deadline,
		Now:      forumContext.Now,
		Sync: func(teamID string, _ []string, options archive.SyncOptions) (archive.SyncReport, error) {
			account, err := newAuthStore(deps).Get(teamID)
			if err != nil {
				return archive.SyncReport{}, errors.New("no stored Slack session for team " + teamID)
			}

			return archive.SyncAccount(teamID, newClient(deps, account),
				archive.NewStore(deps.Paths.ArchiveDir), archive.NewIndex(deps.Paths.ArchiveIndex), options)
		},
	})
}

func forumStatusTool(deps Deps) Handler {
	return func(_ context.Context, _ json.RawMessage) ([]Content, error) {
		forumContext, err := openForumContext(deps)
		if err != nil {
			return nil, failureText(err)
		}

		lines := make([]string, 0, 8)

		who := "unregistered"
		if forumContext.Identity != nil {
			who = forum.LabelOf(forumContext.Identity.Name, forumContext.Identity.Host)
		}

		lines = append(lines, "identity: "+who+" ("+forumContext.IdentityKey+")")

		state, _ := archive.NewStore(deps.Paths.ArchiveDir).LoadState()

		for _, binding := range forumContext.File.Channels {
			status := archive.TeamStatusOf(state, binding.Workspace, time.Now())
			lines = append(lines, "  "+forum.ChannelLabel(binding)+" ("+binding.Channel+") — archive "+
				archive.FormatAge(status.AgeMS)+", since "+binding.SinceTS)
		}

		if len(forumContext.File.Channels) == 0 {
			lines = append(lines, "  no channel bound; add one with `slack-browser agent-forum add <channel>`")
		}

		lines = append(lines, "timings: "+forum.DescribeLimits(forumContext.Limits))

		paths := forum.PathsFor(deps.Paths.ForumDir, deps.Env)

		if forum.Answers(paths.Socket, 300*time.Millisecond) {
			lines = append(lines, "daemon: running at "+paths.Socket)
		} else {
			lines = append(lines, "daemon: not running; the next forum client starts it ("+paths.Socket+")")
		}

		if len(forumContext.File.Ignored) > 0 {
			lines = append(lines, "retired: "+strings.Join(forumContext.File.Ignored, ", "))
		}

		return Text(strings.Join(lines, "\n")), nil
	}
}

func forumInboxTool(deps Deps) Handler {
	return func(_ context.Context, raw json.RawMessage) ([]Content, error) {
		var args struct {
			All         bool `json:"all"`
			WaitSeconds int  `json:"wait_seconds"`
			Offline     bool `json:"offline"`
		}

		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}

		forumContext, err := openForumContext(deps)
		if err != nil {
			return nil, failureText(err)
		}

		note := ""

		if !args.Offline {
			report := refresh(deps, forumContext, forumCatchUp)

			switch {
			case len(report.Errors) > 0:
				note = " · " + report.Errors[0]
			case report.Locked:
				note = " · a sync holds the lock"
			}

			if fresh, err := forumContext.Reload(); err == nil {
				forumContext = fresh
			}
		}

		if lines := forum.DrainSay(deps.Paths.ForumDir, forumContext.MeLabel()); len(lines) > 0 {
			local := make([]string, 0, len(lines))

			for _, line := range lines {
				local = append(local, "  "+line.From+" said: "+line.Text)
			}

			return Text(strings.Join(append([]string{"local lines for this machine (nothing was posted to Slack):"},
				local...), "\n")), nil
		}

		if args.WaitSeconds > 0 {
			result := forum.WaitForWork(forumContext, forum.WaitOptions{
				Seconds: args.WaitSeconds,
				Offline: true,
				All:     args.All,
			})

			return Text(forum.RenderInbox(forumContext, result.Report, args.Offline, note)), nil
		}

		entries := forum.InboxEntries(forumContext, forum.LoadViews(forumContext, forum.ViewFilter{}),
			forum.InboxOptions{All: args.All})

		return Text(forum.RenderInbox(forumContext, entries, args.Offline, note)), nil
	}
}

func forumTaskTool(deps Deps) Handler {
	return func(_ context.Context, raw json.RawMessage) ([]Content, error) {
		var args struct {
			ID      string `json:"id"`
			Offline bool   `json:"offline"`
		}

		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}

		forumContext, err := openForumContext(deps)
		if err != nil {
			return nil, failureText(err)
		}

		if !args.Offline {
			refresh(deps, forumContext, forumCatchUp)

			if fresh, err := forumContext.Reload(); err == nil {
				forumContext = fresh
			}
		}

		located, err := forum.LocateTask(forum.LoadViews(forumContext, forum.ViewFilter{}), args.ID)
		if err != nil {
			return nil, failureText(err)
		}

		decision, decided := located.View.Decisions[located.Task.ID]

		return Text(forum.RenderTask(forumContext, located.Task, located.View.Binding, decision, decided, &located.View)), nil
	}
}

func forumPeersTool(deps Deps) Handler {
	return func(_ context.Context, _ json.RawMessage) ([]Content, error) {
		forumContext, err := openForumContext(deps)
		if err != nil {
			return nil, failureText(err)
		}

		refresh(deps, forumContext, forumCatchUp)

		if fresh, err := forumContext.Reload(); err == nil {
			forumContext = fresh
		}

		lines := make([]string, 0, 8)

		for _, view := range forum.LoadViews(forumContext, forum.ViewFilter{}) {
			lines = append(lines, forum.RenderPeers(view, forumContext.Now, forumContext.Limits)...)
		}

		if len(lines) == 0 {
			return Text("no channel bound; add one with `slack-browser agent-forum add <channel>`"), nil
		}

		return Text(strings.Join(lines, "\n")), nil
	}
}

func forumPostTool(deps Deps) Handler {
	return func(_ context.Context, raw json.RawMessage) ([]Content, error) {
		var args struct {
			To         string `json:"to"`
			Title      string `json:"title"`
			Capability string `json:"capability"`
			Body       string `json:"body"`
			Deadline   string `json:"deadline"`
			Parent     string `json:"parent"`
			Binding    string `json:"binding"`
			Confirm    bool   `json:"confirm"`
		}

		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}

		forumContext, err := openForumContext(deps)
		if err != nil {
			return nil, failureText(err)
		}

		capability := args.Capability

		if capability != "" {
			normalized, err := forum.ParseCapability(capability)
			if err != nil {
				return nil, failureText(err)
			}

			capability = normalized
		}

		result, err := forum.PostTask(forumContext, forumTransports(deps), forum.PostTaskArgs{
			To: args.To, Title: args.Title, Body: args.Body, Capability: capability,
			Deadline: args.Deadline, Parent: args.Parent, Binding: args.Binding,
			Confirm: args.Confirm, By: "agent",
		}, &forum.RefreshHook{Refresh: func() error {
			refresh(deps, forumContext, forumCatchUp)

			return nil
		}})
		if err != nil {
			return nil, failureText(err)
		}

		return Text(renderWriteResult(result)), nil
	}
}

func forumMoveTool(deps Deps, kind string) Handler {
	return func(_ context.Context, raw json.RawMessage) ([]Content, error) {
		var args struct {
			ID      string `json:"id"`
			Body    string `json:"body"`
			OK      *bool  `json:"ok"`
			Confirm bool   `json:"confirm"`
		}

		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}

		forumContext, err := openForumContext(deps)
		if err != nil {
			return nil, failureText(err)
		}

		result, err := forum.ReplyTo(forumContext, forumTransports(deps), forum.ReplyArgs{
			ID: args.ID, Kind: kind, Body: args.Body, OK: args.OK,
			Confirm: args.Confirm, By: "agent",
		}, &forum.RefreshHook{Refresh: func() error {
			refresh(deps, forumContext, forumCatchUp)

			return nil
		}})
		if err != nil {
			return nil, failureText(err)
		}

		return Text(renderWriteResult(result)), nil
	}
}

// renderWriteResult is what an agent reads after a write, or after declining to make
// one: a preview it has to show the user, or where the message landed.
func renderWriteResult(result forum.WriteResult) string {
	where := forum.ChannelLabel(result.Binding)

	warning := ""
	if result.Warning != "" {
		warning = "\nnote: " + result.Warning
	}

	if !result.Posted {
		return "Nothing sent. Show this to the user and post it only after they say yes.\n" +
			"target: " + where + " (" + result.Target + ")" + warning + "\n\n" + result.Text
	}

	if result.TS != "" {
		return "Posted to " + where + " at ts " + result.TS + "; audit: " + result.Audit + warning
	}

	return "Posted to " + where + ", but Slack did not report the message ts; audit: " + result.Audit + warning
}

func forumSayTool(deps Deps) Handler {
	return func(_ context.Context, raw json.RawMessage) ([]Content, error) {
		var args struct {
			To   string `json:"to"`
			Text string `json:"text"`
		}

		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}

		paths := forum.PathsFor(deps.Paths.ForumDir, deps.Env)

		forumContext, err := openForumContext(deps)
		if err != nil {
			return nil, failureText(err)
		}

		identity, err := forumContext.RequireIdentity()
		if err != nil {
			return nil, failureText(err)
		}

		// A local line is a nudge, not the work: the daemon has to be running, because
		// nothing records what was said.
		from := forum.LabelOf(identity.Name, identity.Host)

		if err := forum.DeliverSay(deps.Paths.ForumDir, paths.Socket, args.To, from, args.Text, 2*time.Second); err != nil {
			return nil, failureText(err)
		}

		return Text("delivered to " + args.To + " on this machine (nothing was posted to Slack)"), nil
	}
}

func archiveRefreshTool(deps Deps) Handler {
	return func(_ context.Context, raw json.RawMessage) ([]Content, error) {
		var args struct {
			Team    string `json:"team"`
			Channel string `json:"channel"`
		}

		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}

		forumContext, err := openForumContext(deps)
		if err != nil {
			return nil, failureText(err)
		}

		bindings := forumContext.File.Channels

		if args.Team != "" || args.Channel != "" {
			kept := make([]forum.Binding, 0, len(bindings))

			for _, binding := range bindings {
				if args.Team != "" && binding.Workspace != args.Team {
					continue
				}

				if args.Channel != "" && binding.Channel != args.Channel {
					continue
				}

				kept = append(kept, binding)
			}

			bindings = kept
		}

		report := forum.CatchUp(forum.CatchUpArgs{
			Bindings: bindings,
			Deadline: 20 * time.Second,
			Now:      forumContext.Now,
			Sync: func(teamID string, _ []string, options archive.SyncOptions) (archive.SyncReport, error) {
				account, err := newAuthStore(deps).Get(teamID)
				if err != nil {
					return archive.SyncReport{}, errors.New("no stored Slack session for team " + teamID)
				}

				return archive.SyncAccount(teamID, newClient(deps, account),
					archive.NewStore(deps.Paths.ArchiveDir), archive.NewIndex(deps.Paths.ArchiveIndex), options)
			},
		})

		line := "refreshed " + strconv.Itoa(report.Synced) + " channel(s)"

		if report.Locked {
			line += " · a sync holds the lock"
		}

		if len(report.Errors) > 0 {
			line += " · " + strings.Join(report.Errors, ", ")
		}

		return Text(line), nil
	}
}
