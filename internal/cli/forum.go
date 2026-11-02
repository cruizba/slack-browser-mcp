package cli

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/archive"
	"github.com/cruizba/slack-browser-mcp/internal/buildinfo"
	"github.com/cruizba/slack-browser-mcp/internal/config"
	"github.com/cruizba/slack-browser-mcp/internal/forum"
)

// The forum's moves, named once so a typo cannot become a command nobody can reach.
const (
	subForumList       = "list"
	subForumAway       = "away"
	subForumPost       = "post"
	subForumProgress   = "progress"
	subForumResult     = "result"
	subForumRegister   = "register"
	subForumPeers      = "peers"
	subForumInbox      = "inbox"
	subForumTask       = "task"
	subForumDaemon     = "daemon"
	subForumUnregister = "unregister"
	subForumDelete     = "delete"
	subForumEdit       = "edit"
	subForumWait       = "wait"
	subForumPoll       = "poll"

	// jsonChannel is the key a JSON inbox line carries the channel under.
	jsonChannel = "channel"
)

// forumRefreshDeadline bounds the catch-up a forum command runs before it decides:
// a write has to see what the other side said, and waiting forever for it would make
// a typo in a channel name look like a hang.
const forumRefreshDeadline = 10 * time.Second

// commandForum is the whole `agent-forum` group, with `forum` as its alias.
func commandForum(ctx Context, parsed ParsedArgs) int {
	subcommand := subForumList
	if len(parsed.Positional) > 0 {
		subcommand = parsed.Positional[0]
	}

	rest := parsed.Positional[1:]

	confirm := parsed.Bool("post")
	by := "cli"

	forumContext, err := openForum(ctx)
	if err != nil {
		return fail(ctx, err)
	}

	forum.SetVersion(buildinfo.Version)

	announceUnlessItsOwn(ctx, forumContext, subcommand)

	return runForumMove(ctx, forumContext, parsed, subcommand, rest, confirm, by)
}

// forumMoves is the second half of the dispatch: a map so a new move is one line and
// cannot change the order the others run in.
var forumMoves = map[string]func(Context, forum.Context, ParsedArgs, []string, bool, string) int{
	subForumList: func(ctx Context, fc forum.Context, _ ParsedArgs, _ []string, _ bool, _ string) int {
		return forumList(ctx, fc)
	},
	"add": func(ctx Context, fc forum.Context, parsed ParsedArgs, rest []string, _ bool, _ string) int {
		return forumAdd(ctx, fc, parsed, rest)
	},
	"remove": func(ctx Context, fc forum.Context, _ ParsedArgs, rest []string, _ bool, _ string) int {
		return forumRemove(ctx, fc, rest)
	},
	subForumRegister: func(ctx Context, fc forum.Context, parsed ParsedArgs, rest []string, _ bool, _ string) int {
		return forumRegister(ctx, fc, parsed, rest)
	},
	subForumUnregister: func(ctx Context, fc forum.Context, parsed ParsedArgs, rest []string, _ bool, _ string) int {
		return forumUnregister(ctx, fc, parsed, rest)
	},
	subForumPeers: func(ctx Context, fc forum.Context, parsed ParsedArgs, _ []string, _ bool, _ string) int {
		return forumPeers(ctx, fc, parsed)
	},
	subForumInbox: func(ctx Context, fc forum.Context, parsed ParsedArgs, _ []string, _ bool, _ string) int {
		return forumInbox(ctx, fc, parsed)
	},
	subForumTask: func(ctx Context, fc forum.Context, parsed ParsedArgs, rest []string, _ bool, _ string) int {
		return forumTask(ctx, fc, parsed, rest)
	},
	subForumPost: forumPost,
	subForumAway: func(ctx Context, fc forum.Context, _ ParsedArgs, rest []string, _ bool, _ string) int {
		return forumAway(ctx, fc, rest)
	},
	subForumDelete: func(ctx Context, fc forum.Context, parsed ParsedArgs, rest []string, confirm bool, _ string) int {
		return forumDelete(ctx, fc, parsed, rest, confirm)
	},
	subForumEdit: func(ctx Context, fc forum.Context, parsed ParsedArgs, rest []string, confirm bool, _ string) int {
		return forumEdit(ctx, fc, parsed, rest, confirm)
	},
	subForumWait: func(ctx Context, fc forum.Context, parsed ParsedArgs, _ []string, _ bool, _ string) int {
		return forumWait(ctx, fc, parsed)
	},
	subForumPoll: func(ctx Context, fc forum.Context, parsed ParsedArgs, _ []string, _ bool, _ string) int {
		return forumPoll(ctx, fc, parsed)
	},
}

// runForumMove is the second half of the switch: the moves that act on a task are
// one line because they differ only in the kind they carry.
func runForumMove(
	ctx Context,
	forumContext forum.Context,
	parsed ParsedArgs,
	subcommand string,
	rest []string,
	confirm bool,
	by string,
) int {
	if handler, known := forumMoves[subcommand]; known {
		return handler(ctx, forumContext, parsed, rest, confirm, by)
	}

	switch subcommand {
	case "accept", "decline", subForumProgress, subForumResult, "ack", "reopen", "cancel":
		return forumReply(ctx, forumContext, parsed, subcommand, rest, confirm, by)
	default:
		return fail(ctx, errors.New("`agent-forum "+subcommand+"` is not ported yet"))
	}
}

// announceUnlessItsOwn says this session is here, once per change of state. The
// commands that announce a state of their own would post two messages, so they are
// left out. Best effort: a failed announcement must never fail a command that reads.
func announceUnlessItsOwn(ctx Context, forumContext forum.Context, subcommand string) {
	switch subcommand {
	case "poll", subForumAway, subForumUnregister, subForumRegister, subForumDaemon:
		return
	}

	_, _ = forum.AnnounceLiveness(forumContext, forumTransports(ctx), "session", "", "", false)
}

// openForum assembles the forum state from the paths this run resolved.
func openForum(ctx Context) (forum.Context, error) {
	return forum.OpenForum(forum.OpenOptions{
		Now:       time.Now(),
		Store:     forum.NewStore(ctx.Paths.ForumFile),
		Archive:   archive.NewStore(ctx.Paths.ArchiveDir),
		Decisions: forum.NewDecisionLog(ctx.Paths.ForumDir),
		Env:       ctx.Env,
		AuditLog:  ctx.Paths.AuditLog,
	})
}

// forumTransports builds one transport per workspace, from the stored session.
func forumTransports(ctx Context) forum.TransportResolver {
	return func(teamID string) (forum.Transport, error) {
		account, err := newStore(ctx).Get(teamID)
		if err != nil {
			return forum.Transport{}, err
		}

		settings, err := config.NewStore(ctx.Paths.SettingsFile).Read()
		if err != nil {
			return forum.Transport{}, err
		}

		signing, err := config.SigningFor(settings, ctx.Env)
		if err != nil {
			return forum.Transport{}, err
		}

		return forum.Transport{Account: account, Client: newClient(account), Signing: signing}, nil
	}
}

// refreshForum syncs every bound channel, which is what makes a write decide against
// the thread and not against whatever this machine last saw.
func refreshForum(ctx Context, forumContext forum.Context, deadline time.Duration) forum.CatchUpReport {
	return forum.CatchUp(forum.CatchUpArgs{
		Bindings: forumContext.File.Channels,
		Deadline: deadline,
		Now:      forumContext.Now,
		Sync: func(teamID string, _ []string, options archive.SyncOptions) (archive.SyncReport, error) {
			account, err := newStore(ctx).Get(teamID)
			if err != nil {
				return archive.SyncReport{}, errors.New("no stored Slack session for team " + teamID)
			}

			return archive.SyncAccount(teamID, newClient(account),
				archive.NewStore(ctx.Paths.ArchiveDir), archive.NewIndex(ctx.Paths.ArchiveIndex), options)
		},
	})
}

func forumList(ctx Context, forumContext forum.Context) int {
	who := "unregistered"
	if forumContext.Identity != nil {
		who = forum.LabelOf(forumContext.Identity.Name, forumContext.Identity.Host)
	}

	emit(ctx.Stdout, "agent forum as %s (%s)\n", who, forumContext.IdentityKey)

	if len(forumContext.File.Channels) == 0 {
		emit(ctx.Stdout, "no channel bound; add one with `slack-browser agent-forum add <channel>`\n")
	}

	labels := workspaceLabels(ctx)

	for _, binding := range forumContext.File.Channels {
		scope := labels[binding.Workspace]
		if scope == "" {
			scope = binding.Workspace
		}

		visibility := "public"
		if binding.Private {
			visibility = "private"
		}

		signature := "unsigned"
		if binding.Sign {
			signature = "signed"
		}

		emit(ctx.Stdout, "  %s (%s) — %s\n", forum.ChannelLabel(binding), binding.Channel,
			strings.Join([]string{visibility, scope, signature, "since " + binding.SinceTS}, " · "))
	}

	emit(ctx.Stdout, "timings: %s\n", forum.DescribeLimits(forumContext.Limits))

	if len(forumContext.File.Ignored) > 0 {
		emit(ctx.Stdout, "retired: %s\n", strings.Join(forumContext.File.Ignored, ", "))
	}

	return 0
}

func forumAdd(ctx Context, forumContext forum.Context, parsed ParsedArgs, rest []string) int {
	if len(rest) == 0 {
		return fail(ctx, errors.New("agent-forum add needs a channel name or id"))
	}

	workspace, _ := parsed.String("workspace")

	account, err := newStore(ctx).Resolve(workspace)
	if err != nil {
		return fail(ctx, err)
	}

	transport, err := forumTransports(ctx)(account.TeamID)
	if err != nil {
		return fail(ctx, err)
	}

	binding, added, err := forum.BindChannel(forumContext, transport, forum.BindArgs{
		Reference:  rest[0],
		Backfill:   parsed.Bool("backfill"),
		Signed:     parsed.Bool("signed"),
		NoPresence: parsed.Bool("no-presence"),
	})
	if err != nil {
		return fail(ctx, err)
	}

	if added {
		emit(ctx.Stdout, "bound %s (%s) in %s\n", forum.ChannelLabel(binding), binding.Channel, account.TeamName)
	} else {
		emit(ctx.Stdout, "%s was already bound in %s\n", forum.ChannelLabel(binding), account.TeamName)
	}

	if parsed.Bool("backfill") {
		emit(ctx.Stdout, "reading the whole channel history, oldest first\n")
	} else {
		emit(ctx.Stdout, "reading from ts %s; pass --backfill to read what came before\n", binding.SinceTS)
	}

	if !binding.Announce {
		emit(ctx.Stdout, "this machine will not announce presence in that channel\n")
	}

	if forumContext.Identity == nil {
		emit(ctx.Stdout, "next: pick a name with `slack-browser agent-forum register <name>`\n")
	}

	return 0
}

func forumRemove(ctx Context, forumContext forum.Context, rest []string) int {
	if len(rest) == 0 {
		return fail(ctx, errors.New("agent-forum remove needs a channel name or id"))
	}

	for _, binding := range forumContext.File.Channels {
		if binding.Name != rest[0] && binding.Channel != rest[0] {
			continue
		}

		if _, err := forumContext.Store.Unbind(binding.Workspace, binding.Channel); err != nil {
			return fail(ctx, err)
		}

		emit(ctx.Stdout, "unbound %s (%s); the decision log is kept\n", forum.ChannelLabel(binding), binding.Channel)

		return 0
	}

	return fail(ctx, errors.New("no binding matches \""+rest[0]+"\"; agent-forum list shows them"))
}

func forumRegister(ctx Context, forumContext forum.Context, parsed ParsedArgs, rest []string) int {
	if len(rest) == 0 {
		return fail(ctx, errors.New("agent-forum register needs a name"))
	}

	accepts, _ := parsed.String("accepts")

	capabilities, err := forum.ParseCapabilities(accepts)
	if err != nil {
		return fail(ctx, err)
	}

	host, _ := parsed.String("host")
	if host == "" {
		host = forum.DefaultHost()
	}

	identity := forum.Identity{
		Name:         forum.NormalizeName(rest[0]),
		Host:         forum.NormalizeName(host),
		Capabilities: capabilities,
	}

	if identity.Name == "" {
		return fail(ctx, errors.New("\""+rest[0]+"\" is not a usable name"))
	}

	announce, err := forum.RegisterIdentity(forumContext, identity)
	if err != nil {
		return fail(ctx, err)
	}

	declared := ""
	if len(capabilities) > 0 {
		declared = ", accepting " + strings.Join(capabilities, ", ")
	}

	emit(ctx.Stdout, "registered %s as %s%s\n", forum.LabelOf(identity.Name, identity.Host), forumContext.IdentityKey, declared)

	// The one write that does not ask first: it announces this machine's own name, and
	// a peer nobody can discover is a peer nobody can address.
	if parsed.Bool("no-post") {
		emit(ctx.Stdout, "not announced; peers will not discover this name until you post the register\n")

		return 0
	}

	if !announce {
		emit(ctx.Stdout, "the roster already knows this name and these capabilities; nothing to announce\n")

		return 0
	}

	if len(forumContext.File.Channels) == 0 {
		emit(ctx.Stdout, "no channel bound yet; the announcement goes out the next time you register\n")

		return 0
	}

	result, err := forum.Announce(forumContext, forumTransports(ctx), identity)
	if err != nil {
		return fail(ctx, err)
	}

	line := "announced in " + strconv.Itoa(result.Posted) + " channel(s)"
	if result.Skipped > 0 {
		line += ", " + strconv.Itoa(result.Skipped) + " already knew"
	}

	emit(ctx.Stdout, "%s\n", line)

	return 0
}

func forumUnregister(ctx Context, forumContext forum.Context, parsed ParsedArgs, rest []string) int {
	identity, err := forumContext.RequireIdentity()
	if err != nil {
		return fail(ctx, err)
	}

	if len(rest) > 0 && forum.NormalizeName(rest[0]) != identity.Name {
		return fail(ctx, errors.New("this machine is "+forum.LabelOf(identity.Name, identity.Host)+
			"; unregister \""+rest[0]+"\" is a different peer"))
	}

	label, posted, err := forum.UnregisterIdentity(forumContext, forumTransports(ctx), !parsed.Bool("no-post"))
	if err != nil {
		return fail(ctx, err)
	}

	emit(ctx.Stdout, "unregistered %s\n", label)

	if posted > 0 {
		emit(ctx.Stdout, "announced the departure in %d channel(s); peers drop it from their roster\n", posted)
	} else {
		emit(ctx.Stdout, "nothing announced; peers keep it in the roster until the claim expires\n")
	}

	emit(ctx.Stdout, "register again with `slack-browser agent-forum register <name>` whenever you want\n")

	return 0
}

func forumPeers(ctx Context, forumContext forum.Context, parsed ParsedArgs) int {
	wanted, _ := parsed.String("workspace")

	team := ""

	if wanted != "" {
		account, err := newStore(ctx).Resolve(wanted)
		if err != nil {
			return fail(ctx, err)
		}

		team = account.TeamID
	}

	report := refreshForum(ctx, forumContext, forumRefreshDeadline)

	note := ""

	switch {
	case len(report.Errors) > 0:
		note = "\nnote: " + report.Errors[0]
	case report.Locked:
		note = "\nnote: a sync holds the lock"
	}

	fresh, err := forumContext.Reload()
	if err != nil {
		return fail(ctx, err)
	}

	views := forum.LoadViews(fresh, forum.ViewFilter{Workspace: team})
	if len(views) == 0 {
		emit(ctx.Stdout, "no channel bound; add one with `slack-browser agent-forum add <channel>`\n")

		return 0
	}

	for _, view := range views {
		for _, line := range forum.RenderPeers(view, fresh.Now, fresh.Limits) {
			emit(ctx.Stdout, "%s\n", line)
		}
	}

	if note != "" {
		emit(ctx.Stdout, "%s\n", strings.TrimSpace(note))
	}

	return 0
}

func forumInbox(ctx Context, forumContext forum.Context, parsed ParsedArgs) int {
	offline := parsed.Bool("offline")

	note := ""

	if !offline {
		report := refreshForum(ctx, forumContext, forumRefreshDeadline)

		switch {
		case len(report.Errors) > 0:
			note = " · " + report.Errors[0]
		case report.Locked:
			note = " · a sync holds the lock"
		}
	}

	fresh, err := forumContext.Reload()
	if err != nil {
		return fail(ctx, err)
	}

	views := forum.LoadViews(fresh, forum.ViewFilter{})
	entries := forum.InboxEntries(fresh, views, forum.InboxOptions{All: parsed.Bool("all")})

	if parsed.Bool("json") {
		for _, entry := range entries.Entries {
			line := map[string]any{
				"id":            entry.Task.ID,
				"state":         string(entry.Task.State),
				"local":         nil,
				"self_approved": false,
				"from":          entry.Task.From,
				"to":            entry.Task.To,
				"title":         entry.Task.Title,
				jsonChannel:     entry.View.Binding.Name,
				"ts":            entry.Task.TS,
			}

			if entry.Decided {
				line["local"] = entry.Decision.Decision
				line["self_approved"] = entry.Decision.SelfApproved
			}

			encoded, err := json.Marshal(line)
			if err != nil {
				return fail(ctx, err)
			}

			emit(ctx.Stdout, "%s\n", encoded)
		}

		return 0
	}

	emit(ctx.Stdout, "%s\n", forum.RenderInbox(fresh, entries, offline, note))

	return 0
}

func forumTask(ctx Context, forumContext forum.Context, parsed ParsedArgs, rest []string) int {
	if len(rest) == 0 {
		return fail(ctx, errors.New("agent-forum task needs an id (or a unique prefix)"))
	}

	if !parsed.Bool("offline") {
		refreshForum(ctx, forumContext, forumRefreshDeadline)
	}

	fresh, err := forumContext.Reload()
	if err != nil {
		return fail(ctx, err)
	}

	located, err := forum.LocateTask(forum.LoadViews(fresh, forum.ViewFilter{}), rest[0])
	if err != nil {
		return fail(ctx, err)
	}

	decision, decided := located.View.Decisions[located.Task.ID]

	emit(ctx.Stdout, "%s\n", forum.RenderTask(fresh, located.Task, located.View.Binding, decision, decided, &located.View))

	return 0
}

func forumPost(ctx Context, forumContext forum.Context, parsed ParsedArgs, rest []string, confirm bool, by string) int {
	if len(rest) < 2 {
		return fail(ctx, errors.New("agent-forum post needs a destination and a title"))
	}

	body, _ := parsed.String("body")
	if body == "" {
		body = strings.Join(rest[2:], " ")
	}

	if body == "" {
		return fail(ctx, errors.New("agent-forum post needs a body: pass --body \"<text>\""))
	}

	capability, _ := parsed.String("capability")
	if capability != "" {
		normalized, err := forum.ParseCapability(capability)
		if err != nil {
			return fail(ctx, err)
		}

		capability = normalized
	}

	deadline, _ := parsed.String("deadline")
	parent, _ := parsed.String("parent")
	binding, _ := parsed.String("binding")

	result, err := forum.PostTask(forumContext, forumTransports(ctx), forum.PostTaskArgs{
		To: rest[0], Title: rest[1], Body: body,
		Capability: capability, Deadline: deadline, Parent: parent, Binding: binding,
		Confirm: confirm, By: by,
	}, &forum.RefreshHook{Refresh: func() error {
		refreshForum(ctx, forumContext, forumRefreshDeadline)

		return nil
	}})
	if err != nil {
		return fail(ctx, err)
	}

	return printWrite(ctx, result)
}

func forumReply(
	ctx Context,
	forumContext forum.Context,
	parsed ParsedArgs,
	kind string,
	rest []string,
	confirm bool,
	by string,
) int {
	if len(rest) == 0 {
		return fail(ctx, errors.New("agent-forum "+kind+" needs a task id"))
	}

	body, _ := parsed.String("body")
	if body == "" && (kind == subForumProgress || kind == subForumResult) {
		body = strings.Join(rest[1:], " ")
	}

	var ok *bool

	if kind == subForumResult {
		value := !parsed.Bool("fail")
		ok = &value
	}

	result, err := forum.ReplyTo(forumContext, forumTransports(ctx), forum.ReplyArgs{
		ID: rest[0], Kind: kind, Body: body, OK: ok, Confirm: confirm, By: by,
	}, &forum.RefreshHook{Refresh: func() error {
		refreshForum(ctx, forumContext, forumRefreshDeadline)

		return nil
	}})
	if err != nil {
		return fail(ctx, err)
	}

	return printWrite(ctx, result)
}

func forumAway(ctx Context, forumContext forum.Context, rest []string) int {
	note := strings.TrimSpace(strings.Join(rest, " "))

	result, err := forum.AnnounceLiveness(forumContext, forumTransports(ctx), "session", "away", note, true)
	if err != nil {
		return fail(ctx, err)
	}

	if result.Posted > 0 {
		line := "away"
		if note != "" {
			line += ": " + note
		}

		emit(ctx.Stdout, "%s announced in %d channel(s)\n", line, result.Posted)
	} else {
		emit(ctx.Stdout, "nothing announced; no channel bound or no identity\n")
	}

	return 0
}

// printWrite is how both the callers print what happened, or what would.
func printWrite(ctx Context, result forum.WriteResult) int {
	where := forum.ChannelLabel(result.Binding)

	if result.Warning != "" {
		emit(ctx.Stderr, "note: %s\n", result.Warning)
	}

	if !result.Posted {
		emit(ctx.Stdout, "dry run — would post to %s (%s):\n", where, result.Target)
		emit(ctx.Stdout, "%s\n", result.Text)
		emit(ctx.Stderr, "nothing sent; add --post to commit\n")

		return 0
	}

	if result.TS != "" {
		emit(ctx.Stdout, "sent ts %s to %s; audit: %s\n", result.TS, where, result.Audit)
	} else {
		emit(ctx.Stdout, "sent to %s without a reported ts; audit: %s\n", where, result.Audit)
	}

	return 0
}

func forumDelete(ctx Context, forumContext forum.Context, parsed ParsedArgs, rest []string, confirm bool) int {
	if len(rest) == 0 {
		return fail(ctx, errors.New("agent-forum delete needs a task id or a message ts"))
	}

	refreshForum(ctx, forumContext, forumRefreshDeadline)

	fresh, err := forumContext.Reload()
	if err != nil {
		return fail(ctx, err)
	}

	result, err := forum.DeletePosting(fresh, forumTransports(ctx), forum.RemoveArgs{
		Ref: rest[0], Force: parsed.Bool("force"), Confirm: confirm,
	})
	if err != nil {
		return fail(ctx, err)
	}

	emit(ctx.Stdout, "%s\n", result.Text)

	return 0
}

func forumEdit(ctx Context, forumContext forum.Context, parsed ParsedArgs, rest []string, confirm bool) int {
	body, _ := parsed.String("body")

	if body == "" && len(rest) > 1 {
		body = strings.TrimSpace(strings.Join(rest[1:], " "))
	}

	if len(rest) == 0 || body == "" {
		return fail(ctx, errors.New("agent-forum edit needs a task id and a body: --body \"<text>\""))
	}

	refreshForum(ctx, forumContext, forumRefreshDeadline)

	fresh, err := forumContext.Reload()
	if err != nil {
		return fail(ctx, err)
	}

	result, err := forum.EditTaskBody(fresh, forumTransports(ctx), forum.EditArgs{
		ID: rest[0], Body: body, Confirm: confirm,
	})
	if err != nil {
		return fail(ctx, err)
	}

	emit(ctx.Stdout, "%s\n", result.Text)

	return 0
}

// forumPoll is one refresh pass, and the only place a timer is allowed to cause a
// write: the beacon that says this machine is up.
func forumPoll(ctx Context, forumContext forum.Context, parsed ParsedArgs) int {
	deadlineSeconds, err := numberFlag(parsed, "deadline", 25)
	if err != nil {
		return fail(ctx, err)
	}

	report := refreshForum(ctx, forumContext, time.Duration(deadlineSeconds)*time.Second)

	fresh, err := forumContext.Reload()
	if err != nil {
		return fail(ctx, err)
	}

	announced, _ := forum.AnnounceLiveness(fresh, forumTransports(ctx), "poll", "", "", false)

	if !parsed.Bool("quiet") {
		line := "refreshed " + strconv.Itoa(report.Synced) + " channel(s)"

		if announced.Posted > 0 {
			line += " · beacon in " + strconv.Itoa(announced.Posted)
		}

		if len(report.Errors) > 0 {
			line += " · " + strings.Join(report.Errors, " · ")
		}

		emit(ctx.Stdout, "%s\n", line)
	}

	if len(report.Errors) > 0 {
		return 1
	}

	return 0
}

// forumWait blocks until work lands, or prints what is already there.
func forumWait(ctx Context, forumContext forum.Context, parsed ParsedArgs) int {
	offline := parsed.Bool("offline")
	asJSON := parsed.Bool("json")
	follow := parsed.Bool("follow")

	timeout, err := numberFlag(parsed, "timeout", map[bool]int{true: 30, false: 0}[follow])
	if err != nil {
		return fail(ctx, err)
	}

	seen := map[string]bool{}

	for {
		result := forum.WaitForWork(forumContext, forum.WaitOptions{
			Seconds: timeout,
			Offline: offline,
			Refresh: func() forum.CatchUpReport {
				return refreshForum(ctx, forumContext, forumRefreshDeadline)
			},
		})

		for _, entry := range result.Report.Entries {
			if seen[entry.Task.ID] {
				continue
			}

			seen[entry.Task.ID] = true

			if asJSON {
				line := map[string]any{
					"event": "task", "id": entry.Task.ID, "from": entry.Task.From,
					"to": entry.Task.To, "title": entry.Task.Title,
					jsonChannel: entry.View.Binding.Name, "ts": entry.Task.TS,
				}

				encoded, err := json.Marshal(line)
				if err != nil {
					return fail(ctx, err)
				}

				emit(ctx.Stdout, "%s\n", encoded)

				continue
			}

			emit(ctx.Stdout, "%s  %s  %s  %s: %s\n", entry.Task.ID,
				forum.StateLabel(entry.Task, forumContext.Now),
				forum.DecisionLabel(entry.Decision, entry.Decided),
				entry.Task.From, entry.Task.Title)
		}

		if !follow {
			if len(result.Report.Entries) == 0 {
				emit(ctx.Stdout, "nothing pending\n")
			}

			return 0
		}

		time.Sleep(time.Duration(max(5, timeout)) * time.Second)
	}
}
