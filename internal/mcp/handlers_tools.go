package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/archive"
	"github.com/cruizba/slack-browser-mcp/internal/config"
	"github.com/cruizba/slack-browser-mcp/internal/platform"
	"github.com/cruizba/slack-browser-mcp/internal/slack"
)

// freshness is the age of what a read is about to serve, plus the background sync it
// may start when that age is too large for the question. A read never waits for the
// sync: it reports the age and answers from what it has.
type freshnessState struct {
	age   *int64
	note  string
	entry archive.ChannelState
	known bool
}

func freshness(deps Deps, teamID, channelID string, atNow bool) freshnessState {
	store := archive.NewStore(deps.Paths.ArchiveDir)

	state, err := store.LoadState()
	if err != nil {
		return freshnessState{}
	}

	result := freshnessState{}
	result.entry, result.known, result.age = lastSync(state, teamID, channelID)

	settings, err := config.NewStore(deps.Paths.SettingsFile).Read()
	if err != nil {
		return result
	}

	policy, err := config.Freshness(settings, deps.Env)
	if err != nil {
		return result
	}

	result.note = nudgeSync(deps, wantsRefresh(result.age, atNow, policy))

	return result
}

// lastSync answers when a channel was last walked: its own stamp, or the workspace's
// when the channel has none.
func lastSync(state archive.SyncState, teamID, channelID string) (archive.ChannelState, bool, *int64) {
	team, known := state.Teams[teamID]
	if !known {
		return archive.ChannelState{}, false, nil
	}

	entry := archive.ChannelState{}
	hasEntry := false

	if channelID != "" {
		entry, hasEntry = team.Channels[channelID]
	}

	stamp := entry.LastSync
	if stamp == "" {
		stamp = team.LastRun
	}

	if stamp == "" {
		return entry, hasEntry, nil
	}

	when, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return entry, hasEntry, nil
	}

	age := max(int64(0), time.Since(when).Milliseconds())

	return entry, hasEntry, &age
}

// nudgeSync is the fallback for a machine with no daemon: one detached sync child,
// throttled so a failing sync cannot be restarted by every read.
func nudgeSync(deps Deps, wanted bool) string {
	if !wanted || noAutoSync(deps) || deps.Self == "" {
		return ""
	}

	lock := archive.NewSyncLock(deps.Paths.ArchiveDir)

	if owner, _ := lock.Owner(); owner != nil {
		return " (a sync is already running)"
	}

	stamp := filepath.Join(deps.Paths.ArchiveDir, ".nudge")

	if info, err := os.Stat(stamp); err == nil && time.Since(info.ModTime()) < nudgeCooldown {
		return ""
	}

	if err := os.MkdirAll(deps.Paths.ArchiveDir, 0o700); err != nil {
		return ""
	}

	if err := os.WriteFile(stamp, []byte(time.Now().UTC().Format(time.RFC3339)), 0o600); err != nil {
		return ""
	}

	command := exec.CommandContext(context.Background(), deps.Self, "sync", "--quiet", "--deadline", "180") //nolint:gosec // the binary is this one
	command.Stdout = nil
	command.Stderr = nil
	command.Stdin = nil

	if err := command.Start(); err != nil {
		return ""
	}

	_ = command.Process.Release()

	return " (background sync started)"
}

func noAutoSync(deps Deps) bool {
	value, present := platform.Lookup(deps.Env, "SLACK_BROWSER_MCP_NO_AUTOSYNC")

	return present && value == "1"
}

// remember files what a read fetched, so asking twice is cheap. A failed write never
// fails the read that already has its answer.
func remember(deps Deps, teamID string, channel slack.Channel, messages []slack.Message, source string) {
	if len(messages) == 0 {
		return
	}

	store := archive.NewStore(deps.Paths.ArchiveDir)

	cached, err := store.Read(teamID, channel.ID)
	if err != nil {
		return
	}

	byTS := make(map[string]archive.ArchivedMessage, len(cached))
	for _, record := range cached {
		byTS[record.TS] = record
	}

	records := make([]archive.ArchivedMessage, 0, len(messages))

	now := time.Now()

	for _, message := range messages {
		record := archive.ToRecord(channel, message, now, source)

		cachedRecord, known := byTS[message.TS]

		var pointer *archive.ArchivedMessage
		if known {
			pointer = &cachedRecord
		}

		if !archive.Changed(pointer, record) {
			continue
		}

		records = append(records, record)
	}

	if len(records) == 0 {
		return
	}

	if err := store.Append(teamID, channel.ID, records); err != nil {
		return
	}

	index := archive.NewIndex(deps.Paths.ArchiveIndex)

	defer func() { _ = index.Close() }()

	_ = index.Upsert(teamID, records)
}

// archiveChannel is the channel as the archive names it. Slack lists a DM under the
// peer's user id, and an id passed as a name is not a name: storing it would relabel
// the channel file.
func archiveChannel(target slack.Channel, directory *slack.Directory) slack.Channel {
	entry := directory.Channel(target.ID)

	name := ""
	if entry != nil {
		name = entry.Name
	}

	if name == "" && target.Name != "" && target.Name != target.ID {
		name = target.Name
	}

	return slack.Channel{ID: target.ID, Name: name, User: target.User}
}

func readMessages(deps Deps) Handler {
	return func(_ context.Context, raw json.RawMessage) ([]Content, error) {
		var args struct {
			Workspace string `json:"workspace"`
			Channel   string `json:"channel"`
			Limit     int    `json:"limit"`
			Oldest    string `json:"oldest"`
			Latest    string `json:"latest"`
		}

		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}

		if args.Limit == 0 {
			args.Limit = 20
		}

		ensureSchema(deps)

		session, err := openSession(deps, args.Workspace)
		if err != nil {
			return nil, failureText(err)
		}

		memo := callDirectory(deps, session.account, session.client)
		directory := memo.Get()

		target, err := resolveTarget(session, directory, args.Channel)
		if err != nil {
			return nil, failureText(err)
		}

		fresh := freshness(deps, session.account.TeamID, target.ID, args.Latest == "")
		origin := "archive: " + formatAge(fresh.age) + fresh.note
		label := slack.ChannelLabel(target, directory)

		cached, err := archive.LocalHistory(archive.LocalHistoryArgs{
			Store: archive.NewStore(deps.Paths.ArchiveDir), TeamID: session.account.TeamID,
			ChannelID: target.ID, Limit: args.Limit, Oldest: args.Oldest, Latest: args.Latest,
			Synced: fresh.known && fresh.entry.LastSync != "",
		})
		if err != nil {
			return nil, failureText(err)
		}

		if cached != nil {
			return Text(cachedAnswer(session, directory, target, label, cached, origin, fresh, args.Limit)), nil
		}

		page, err := session.client.History(slack.HistoryParams{
			Channel: target.ID, Limit: args.Limit, Oldest: args.Oldest, Latest: args.Latest,
		})
		if err != nil {
			return nil, failureText(err)
		}

		remember(deps, session.account.TeamID, archiveChannel(target, directory), page.Messages, "")

		if len(page.Messages) == 0 {
			return Text(session.account.TeamName + " · " + label + ": no messages in that range (from Slack)."), nil
		}

		chronological := make([]slack.Message, 0, len(page.Messages))

		for _, message := range slices.Backward(page.Messages) {
			chronological = append(chronological, message)
		}

		body := renderPage(session, directory, chronological)

		footer := ""
		if page.HasMore {
			footer = "\n\nmore history exists; call again with oldest=" + chronological[0].TS
		}

		return Text(session.account.TeamName + " · " + label + " (" + target.ID + ") — " +
			strconv.Itoa(len(chronological)) + " messages, oldest first · from Slack\n" + body + footer), nil
	}
}

// cachedAnswer is the same page served from disk, with how old it is and how far back
// the archive reaches.
func cachedAnswer(
	session session,
	directory *slack.Directory,
	target slack.Channel,
	label string,
	messages []slack.Message,
	origin string,
	fresh freshnessState,
	limit int,
) string {
	if len(messages) == 0 {
		return session.account.TeamName + " · " + label + ": no messages in that range (" + origin + ")."
	}

	body := renderPage(session, directory, messages)

	coverage := ""

	if fresh.entry.OldestTS != "" && len(messages) < limit {
		coverage = "\n\nthis is everything the archive holds for the channel, from " +
			slack.FormatTS(fresh.entry.OldestTS)
	}

	return session.account.TeamName + " · " + label + " (" + target.ID + ") — " +
		strconv.Itoa(len(messages)) + " messages, oldest first · " + origin + "\n" + body + coverage
}

func renderPage(session session, directory *slack.Directory, messages []slack.Message) string {
	names := session.client.NamesFor(messages, func(id string) (string, bool) {
		if label := directory.User(id); label != "" {
			return label, true
		}

		return "", false
	})

	return slack.RenderMessages(messages, slack.RenderOptions{Names: names, Scope: directory.Scope()})
}

// resolveTarget answers a channel reference from the directory when it can, and asks
// Slack only when it cannot.
func resolveTarget(session session, directory *slack.Directory, reference string) (slack.Channel, error) {
	if target, known := slack.DirectoryConversation(directory, reference); known {
		return target, nil
	}

	return session.client.ResolveChannel(reference, func(name string) (string, bool) {
		if directory == nil {
			return "", false
		}

		user := directory.UserByReference(name)
		if user == nil {
			return "", false
		}

		return user.ID, true
	})
}

func ensureSchema(deps Deps) {
	_, _ = archive.EnsureSchema(deps.Paths.ArchiveDir, func(string) {})
}

func getThread(deps Deps) Handler {
	return func(_ context.Context, raw json.RawMessage) ([]Content, error) {
		var args struct {
			Workspace string `json:"workspace"`
			Channel   string `json:"channel"`
			ThreadTS  string `json:"thread_ts"`
			Limit     int    `json:"limit"`
		}

		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}

		if args.Limit == 0 {
			args.Limit = 100
		}

		ensureSchema(deps)

		session, err := openSession(deps, args.Workspace)
		if err != nil {
			return nil, failureText(err)
		}

		memo := callDirectory(deps, session.account, session.client)
		directory := memo.Get()

		target, err := resolveTarget(session, directory, args.Channel)
		if err != nil {
			return nil, failureText(err)
		}

		fresh := freshness(deps, session.account.TeamID, target.ID, false)

		cached, err := archive.LocalThread(archive.LocalThreadArgs{
			Store: archive.NewStore(deps.Paths.ArchiveDir), TeamID: session.account.TeamID,
			ChannelID: target.ID, ThreadTS: args.ThreadTS, Limit: args.Limit,
		})
		if err != nil {
			return nil, failureText(err)
		}

		messages := cached

		if messages == nil {
			messages, err = session.client.Replies(target.ID, args.ThreadTS)
			if err != nil {
				return nil, failureText(err)
			}

			remember(deps, session.account.TeamID, archiveChannel(target, directory), messages, "")
		}

		origin := "from Slack"
		if cached != nil {
			origin = "archive: " + formatAge(fresh.age) + fresh.note
		}

		header := session.account.TeamName + " · thread in " + slack.ChannelLabel(target, directory) +
			" (" + target.ID + ") — " + strconv.Itoa(len(messages)) + " messages · " + origin

		return Text(header + "\n" + renderPage(session, directory, messages)), nil
	}
}

func archiveStatus(deps Deps) Handler {
	return func(_ context.Context, raw json.RawMessage) ([]Content, error) {
		var args struct {
			Workspace string `json:"workspace"`
		}

		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}

		schema, err := archive.EnsureSchema(deps.Paths.ArchiveDir, func(string) {})
		if err != nil {
			return nil, failureText(err)
		}

		store := archive.NewStore(deps.Paths.ArchiveDir)

		state, err := store.LoadState()
		if err != nil {
			return nil, failureText(err)
		}

		teamIDs := teamList(deps, args.Workspace, state)
		if len(teamIDs) == 0 {
			return Text("No archive yet. Run `slack-browser sync`, or read a channel: the first read fetches and files it."), nil
		}

		now := time.Now()
		labels := workspaceLabels(deps)

		lines := make([]string, 0, len(teamIDs)+4)

		for _, teamID := range teamIDs {
			status := archive.TeamStatusOf(state, teamID, now)

			errorsText := "none"

			if len(status.Errors) > 0 {
				parts := make([]string, 0, len(status.Errors))
				for _, problem := range status.Errors {
					parts = append(parts, problem.Channel+": "+problem.Error)
				}

				errorsText = strings.Join(parts, "; ")
			}

			lines = append(lines, labelFor(labels, teamID)+" — last sync "+formatAge(status.AgeMS)+
				", "+strconv.Itoa(status.Channels)+" channels, "+strconv.Itoa(status.PendingThreads)+
				" thread parents pending, errors: "+errorsText)
		}

		index := archive.NewIndex(deps.Paths.ArchiveIndex)

		if stats, err := index.Stats(""); err == nil {
			lines = append(lines, "index: "+strconv.Itoa(stats.Messages)+" messages ("+
				strconv.Itoa(stats.Deleted)+" deleted) in "+strconv.Itoa(stats.Channels)+" channels")
		}

		_ = index.Close()

		if schema.Locked {
			lines = append(lines, "schema: v"+strconv.Itoa(schema.From)+" (migration waiting for the running sync)")
		} else {
			lines = append(lines, "schema: v"+strconv.Itoa(schema.To))
		}

		syncLock := archive.NewSyncLock(deps.Paths.ArchiveDir)

		if owner, _ := syncLock.Owner(); owner != nil {
			lines = append(lines, "sync: running since "+
				owner.StartedAt.UTC().Format("2006-01-02T15:04:05.000Z")+" (pid "+strconv.Itoa(owner.PID)+")")
		} else {
			lines = append(lines, "sync: idle")
		}

		settings, err := config.NewStore(deps.Paths.SettingsFile).Read()
		if err == nil {
			if policy, err := config.Freshness(settings, deps.Env); err == nil {
				lines = append(lines, config.DescribePolicy(policy))
			}
		}

		return Text(strings.Join(lines, "\n")), nil
	}
}

// teamList is the workspaces a question covers: one when it names one, every synced
// one otherwise.
func teamList(deps Deps, reference string, state archive.SyncState) []string {
	if reference != "" {
		account, err := newAuthStore(deps).Resolve(reference)
		if err != nil {
			return nil
		}

		return []string{account.TeamID}
	}

	teams := make([]string, 0, len(state.Teams))

	for teamID := range state.Teams {
		teams = append(teams, teamID)
	}

	sort.Strings(teams)

	return teams
}

func workspaceLabels(deps Deps) map[string]string {
	labels := map[string]string{}

	summaries, err := newAuthStore(deps).List()
	if err != nil {
		return labels
	}

	for _, summary := range summaries {
		labels[summary.TeamID] = summary.TeamName
	}

	return labels
}

func labelFor(labels map[string]string, teamID string) string {
	if name := labels[teamID]; name != "" && name != teamID {
		return name + " (" + teamID + ")"
	}

	return teamID
}
