package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/archive"
	"github.com/cruizba/slack-browser-mcp/internal/config"
	"github.com/cruizba/slack-browser-mcp/internal/forum"
	"github.com/cruizba/slack-browser-mcp/internal/slack"
)

// forumAgentName is the name a generated signature carries: the client behind the
// call, which the identity layer already knows how to read.
func forumAgentName(deps Deps) string {
	return forum.AgentName(deps.Env, "", "")
}

// archiveMessages is one linear transcript format, like the help page: the branches
// are alternative columns of the same line, and splitting them would separate the
// format from the values it renders.
//
//nolint:gocyclo,funlen // one transcript format, read top to bottom
func archiveMessages(deps Deps) Handler {
	return func(_ context.Context, raw json.RawMessage) ([]Content, error) {
		var args struct {
			Workspace      string `json:"workspace"`
			Order          string `json:"order"`
			Limit          int    `json:"limit"`
			Author         string `json:"author"`
			Channel        string `json:"channel"`
			Oldest         string `json:"oldest"`
			Latest         string `json:"latest"`
			IncludeDeleted bool   `json:"include_deleted"`
			IncludeReplies bool   `json:"include_replies"`
		}

		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}

		if args.Order == "" {
			args.Order = "oldest"
		}

		if args.Limit == 0 {
			args.Limit = 20
		}

		ensureSchema(deps)

		store := archive.NewStore(deps.Paths.ArchiveDir)

		state, err := store.LoadState()
		if err != nil {
			return nil, failureText(err)
		}

		age := archive.OldestAge(state, time.Now())

		teamIDs := teamList(deps, args.Workspace, state)
		if len(teamIDs) == 0 {
			// state.json is sync's bookkeeping; a rebuilt archive can lack it while the
			// messages sit there, and refusing to read them would be a lie.
			teamIDs, _ = store.Teams()
		}

		if len(teamIDs) == 0 {
			return Text("No archive yet. Run `slack-browser sync`, or read a channel: the first read fetches and files it."), nil
		}

		index := archive.NewIndex(deps.Paths.ArchiveIndex)

		defer func() { _ = index.Close() }()

		hits := make([]archive.SearchHit, 0, args.Limit*len(teamIDs))
		matched := make([]string, 0, 1)

		for _, teamID := range teamIDs {
			directory, _ := slack.NewDirectoryStore(deps.Paths.ArchiveDir).Read(teamID)

			var authors []string

			if args.Author != "" {
				people := matchAuthors(directory, args.Author)

				if len(people) > 1 {
					names := make([]string, 0, len(people))
					for _, user := range people {
						names = append(names, slack.UserLabel(&user))
					}

					return Text("\"" + args.Author + "\" matches " + strconv.Itoa(len(people)) +
						" people: " + strings.Join(names, ", ") + ". Pass one of those names or an id."), nil
				}

				if len(people) == 0 {
					continue
				}

				matched = append(matched, slack.UserLabel(&people[0]))
				authors = []string{people[0].ID}
			}

			filter := slack.ConversationFilter{}
			if args.Channel != "" {
				filter = slack.FilterOf(args.Channel, []*slack.Directory{directory})
			}

			found, err := index.List(archive.ListParams{
				Team: teamID, Limit: args.Limit, Newest: args.Order == "newest",
				IncludeDeleted: args.IncludeDeleted, IncludeReplies: args.IncludeReplies,
				Authors: authors, Channel: filter.Channel, ChannelName: filter.ChannelName,
				Oldest: args.Oldest, Latest: args.Latest,
			})
			if err != nil {
				return nil, failureText(err)
			}

			hits = append(hits, found...)
		}

		if args.Author != "" && len(matched) == 0 {
			return nil, errors.New("No user in the cached directory matches \"" + args.Author +
				"\". Names come from the last sync, so try the id, or list_conversations to see the spellings in use.")
		}

		sort.SliceStable(hits, func(left, right int) bool {
			if args.Order == "newest" {
				return hits[right].TS < hits[left].TS || compareTS(hits[left].TS, hits[right].TS) > 0
			}

			return compareTS(hits[left].TS, hits[right].TS) < 0
		})

		page := hits
		if len(page) > args.Limit {
			page = page[:args.Limit]
		}

		labels := map[string]string{}
		if len(teamIDs) > 1 {
			labels = workspaceLabels(deps)
		}

		lines := make([]string, 0, len(page))

		for _, hit := range page {
			directory, _ := slack.NewDirectoryStore(deps.Paths.ArchiveDir).Read(hit.Team)
			label := slack.ConversationLabel(slack.ConversationRef{ID: hit.Channel, Name: hit.ChannelName}, directory)

			who := hit.Username
			if who == "" {
				who = directory.User(hit.User)
			}

			if who == "" {
				who = hit.User
			}

			if who == "" {
				who = "unknown"
			}

			text := slack.ResolveReferences(hit.Text, directory.Scope())

			thread := ""
			if hit.ReplyCount != nil && *hit.ReplyCount > 0 {
				thread = " (" + strconv.Itoa(*hit.ReplyCount) + " replies, thread_ts " + hit.TS + ")"
			} else if hit.ThreadTS != "" {
				thread = " (thread " + hit.ThreadTS + ")"
			}

			scope := labels[hit.Team]

			prefix := ""
			if scope != "" && scope != hit.Team {
				prefix = scope + " · "
			}

			deleted := ""
			if hit.Deleted {
				deleted = " [deleted]"
			}

			lines = append(lines, "["+hit.TS+"] "+slack.FormatTS(hit.TS)+" "+prefix+label+" "+
				who+": "+text+thread+deleted)
		}

		scopedTeam := ""
		if len(teamIDs) == 1 {
			scopedTeam = teamIDs[0]
		}

		stats, _ := index.Stats(scopedTeam)

		header := "archive: " + archive.FormatAge(age) + " · " + args.Order + " first · " +
			strconv.Itoa(len(lines)) + " of " + strconv.Itoa(stats.Messages) + " archived messages"

		if len(matched) > 0 {
			header += " by " + strings.Join(matched, ", ")
		}

		if len(lines) == 0 {
			return Text(header + "\nNothing in the archive matches. It covers " +
				formatOrNone(stats.Oldest) + " to " + formatOrNone(stats.Newest) +
				": a gap means the range was never synced, not that nothing was said."), nil
		}

		coverage := ""
		if len(hits) <= len(lines) {
			coverage = "\n\nthat is everything the archive holds: " + strconv.Itoa(stats.Messages) +
				" messages, " + formatOrNone(stats.Oldest) + " to " + formatOrNone(stats.Newest) +
				". Going further back than that needs `slack-browser sync --full --since <days>`."
		}

		return Text(header + "\n" + strings.Join(lines, "\n") + coverage), nil
	}
}

func compareTS(left, right string) int {
	leftValue, errLeft := strconv.ParseFloat(left, 64)
	rightValue, errRight := strconv.ParseFloat(right, 64)

	if errLeft != nil || errRight != nil {
		return strings.Compare(left, right)
	}

	switch {
	case leftValue < rightValue:
		return -1
	case leftValue > rightValue:
		return 1
	default:
		return 0
	}
}

func formatOrNone(ts string) string {
	if ts == "" {
		return "nothing"
	}

	return slack.FormatTS(ts)
}

// matchAuthors is who the user means by a name, from the cached directory only:
// resolving against Slack would cost one lookup per hit.
func matchAuthors(directory *slack.Directory, author string) []slack.DirectoryUser {
	if directory == nil {
		return nil
	}

	needle := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(author), "@"))

	exact := make([]slack.DirectoryUser, 0, 2)

	for _, user := range directory.Users {
		for _, field := range []string{user.ID, user.Name, user.DisplayName, user.RealName} {
			if field != "" && strings.ToLower(field) == needle {
				exact = append(exact, user)

				break
			}
		}
	}

	if len(exact) > 0 {
		return exact
	}

	partial := make([]slack.DirectoryUser, 0, 4)

	for _, user := range directory.Users {
		for _, field := range []string{user.Name, user.DisplayName, user.RealName} {
			if field != "" && strings.Contains(strings.ToLower(field), needle) {
				partial = append(partial, user)

				break
			}
		}
	}

	return partial
}

// searchMessages reads one page the same way, local first and Slack second.
//
//nolint:gocyclo,funlen // one transcript format, read top to bottom
func searchMessages(deps Deps) Handler {
	return func(_ context.Context, raw json.RawMessage) ([]Content, error) {
		var args struct {
			Query          string `json:"query"`
			Workspace      string `json:"workspace"`
			Channel        string `json:"channel"`
			Limit          int    `json:"limit"`
			IncludeDeleted bool   `json:"include_deleted"`
		}

		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}

		if args.Limit == 0 {
			args.Limit = 20
		}

		ensureSchema(deps)

		store := archive.NewStore(deps.Paths.ArchiveDir)

		state, err := store.LoadState()
		if err != nil {
			return nil, failureText(err)
		}

		age := archive.OldestAge(state, time.Now())

		settings, err := config.NewStore(deps.Paths.SettingsFile).Read()
		if err != nil {
			return nil, failureText(err)
		}

		policy, err := config.Freshness(settings, deps.Env)
		if err != nil {
			return nil, failureText(err)
		}

		note := nudgeSync(deps, wantsRefresh(age, false, policy))
		freshnessLine := "archive: " + archive.FormatAge(age) + note

		team := ""

		if args.Workspace != "" {
			account, err := newAuthStore(deps).Resolve(args.Workspace)
			if err != nil {
				return nil, failureText(err)
			}

			team = account.TeamID
		}

		index := archive.NewIndex(deps.Paths.ArchiveIndex)

		defer func() { _ = index.Close() }()

		filter := slack.ConversationFilter{}

		if args.Channel != "" {
			directories := make([]*slack.Directory, 0, 2)

			if team != "" {
				directory, _ := slack.NewDirectoryStore(deps.Paths.ArchiveDir).Read(team)
				directories = append(directories, directory)
			} else {
				summaries, _ := newAuthStore(deps).List()
				for _, summary := range summaries {
					directory, _ := slack.NewDirectoryStore(deps.Paths.ArchiveDir).Read(summary.TeamID)
					directories = append(directories, directory)
				}
			}

			filter = slack.FilterOf(args.Channel, directories)
		}

		hits, err := index.Search(archive.SearchParams{
			Query: args.Query, Limit: args.Limit, IncludeDeleted: args.IncludeDeleted,
			Team: team, Channel: filter.Channel, ChannelName: filter.ChannelName,
		})
		if err != nil {
			return nil, failureText(err)
		}

		if len(hits) > 0 {
			return Text(freshnessLine + "\n" + strconv.Itoa(len(hits)) + " hits (best first)\n" +
				formatHits(deps, hits, team == "")), nil
		}

		session, err := openSession(deps, args.Workspace)
		if err != nil {
			return nil, failureText(err)
		}

		remote, err := session.client.SearchMessages(args.Query, args.Limit, 0)
		if err != nil {
			return nil, failureText(err)
		}

		wanted := strings.ToLower(args.Channel)
		matches := remote.Matches

		if wanted != "" {
			filtered := make([]slack.SearchMatch, 0, len(matches))

			for _, match := range matches {
				name := ""
				id := ""

				if match.Channel != nil {
					name = strings.ToLower(match.Channel.Name)
					id = match.Channel.ID
				}

				if name == strings.TrimPrefix(wanted, "#") || id == args.Channel {
					filtered = append(filtered, match)
				}
			}

			if len(filtered) == 0 && len(remote.Matches) > 0 {
				return Text(freshnessLine + "\nNo local hit, and none of the " + strconv.Itoa(remote.Total) +
					" Slack search hits are in \"" + args.Channel + "\". Search again without channel to see them."), nil
			}

			matches = filtered
		}

		if len(matches) == 0 {
			return Text(freshnessLine + "\nNo archived message matches \"" + args.Query +
				"\", and Slack search returned nothing either."), nil
		}

		lines, buckets := searchBuckets(matches)

		for channel, messages := range buckets {
			remember(deps, session.account.TeamID, channel, messages, "search")
		}

		return Text(freshnessLine + "\n" + strconv.Itoa(len(matches)) + " of " + strconv.Itoa(remote.Total) +
			" hits from Slack search, now archived\n" + strings.Join(lines, "\n")), nil
	}
}

// searchBuckets groups Slack's hits by channel, because the archive files them per
// channel and a DM is filed under the peer's id.
func searchBuckets(matches []slack.SearchMatch) ([]string, map[slack.Channel][]slack.Message) {
	lines := make([]string, 0, len(matches))
	buckets := map[slack.Channel][]slack.Message{}

	for _, match := range matches {
		if match.Channel == nil || match.Channel.ID == "" {
			continue
		}

		channelID := match.Channel.ID

		label := "#" + match.Channel.Name
		if match.Channel.Name == "" {
			label = channelID
		}

		author := match.Username
		if author == "" {
			author = match.User
		}

		if author == "" {
			author = "unknown"
		}

		lines = append(lines, "["+match.TS+"] "+label+" "+author+": "+match.Text)

		channel := slack.Channel{ID: channelID}
		if match.Channel.Name != "" && !strings.HasPrefix(channelID, "D") {
			channel.Name = match.Channel.Name
		}

		buckets[channel] = append(buckets[channel], slack.Message{
			TS: match.TS, Text: match.Text, User: match.User,
			Username: match.Username, ThreadTS: match.ThreadTS,
		})
	}

	return lines, buckets
}

func formatHits(deps Deps, hits []archive.SearchHit, labelTeams bool) string {
	labels := map[string]string{}
	if labelTeams {
		labels = workspaceLabels(deps)
	}

	lines := make([]string, 0, len(hits))

	for _, hit := range hits {
		directory, _ := slack.NewDirectoryStore(deps.Paths.ArchiveDir).Read(hit.Team)

		label := slack.ConversationLabel(slack.ConversationRef{ID: hit.Channel, Name: hit.ChannelName}, directory)

		author := hit.Username
		if author == "" {
			author = directory.User(hit.User)
		}

		if author == "" {
			author = hit.User
		}

		if author == "" {
			author = "unknown"
		}

		thread := ""
		if hit.ThreadTS != "" {
			thread = " (thread " + hit.ThreadTS + ")"
		}

		deleted := ""
		if hit.Deleted {
			deleted = " [deleted]"
		}

		prefix := ""
		if scope := labels[hit.Team]; scope != "" {
			prefix = scope + " · "
		}

		lines = append(lines, "["+hit.TS+"] "+prefix+label+" "+author+": "+
			slack.ResolveReferences(hit.Text, directory.Scope())+thread+deleted)
	}

	return strings.Join(lines, "\n")
}

func readFile(deps Deps) Handler {
	return func(_ context.Context, raw json.RawMessage) ([]Content, error) {
		var args struct {
			Workspace string `json:"workspace"`
			File      string `json:"file"`
			Refresh   bool   `json:"refresh"`
		}

		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}

		session, err := openSession(deps, args.Workspace)
		if err != nil {
			return nil, failureText(err)
		}

		stored, info, err := storeAttachment(session, deps, args.File, args.Refresh)
		if err != nil {
			return nil, failureText(err)
		}

		mimeType := imageMime(stored.Name, info.Mimetype)
		isImage := strings.HasPrefix(mimeType, "image/")

		summary := session.account.TeamName + " · " + stored.Name + " (" + args.File + ", " +
			orUnknownType(mimeType) + ", " + slack.HumanSize(stored.Bytes) + ")\n" + stored.Path

		if isImage && stored.Bytes <= maxInlineImageBytes {
			image, err := base64Image(stored.Path, mimeType)
			if err != nil {
				return nil, failureText(err)
			}

			return append(Text(summary), image...), nil
		}

		note := "\nNot an image: read it from that path if the content matters."
		if isImage {
			note = "\nToo large to inline (limit " + slack.HumanSize(maxInlineImageBytes) + "); the file is on disk."
		}

		return Text(summary + note), nil
	}
}

// storeAttachment answers the copy in the archive, downloading it once when there is
// none: a download is on demand because nothing large moves until it is asked for.
func storeAttachment(session session, deps Deps, fileID string, refresh bool) (*archive.StoredFile, slack.File, error) {
	files := archive.NewFileStore(deps.Paths.ArchiveDir)

	var stored *archive.StoredFile

	if !refresh {
		stored, _ = files.Find(session.account.TeamID, fileID)
	}

	var info slack.File

	fetched, fetchErr := session.client.FileInfo(fileID)
	if fetchErr == nil {
		info = fetched
	} else if stored == nil {
		return nil, slack.File{}, fetchErr
	}

	if stored != nil {
		return stored, info, nil
	}

	target := info.URLPrivateDownload
	if target == "" {
		target = info.URLPrivate
	}

	if target == "" {
		return nil, slack.File{}, errors.New("Slack returned no download URL for " + fileID +
			"; it may be an external link preview or a deleted file.")
	}

	bytes, _, err := session.client.FetchAttachment(target)
	if err != nil {
		return nil, slack.File{}, err
	}

	file := info
	file.ID = fileID

	stored, err = files.Save(session.account.TeamID, file, bytes)
	if err != nil {
		return nil, slack.File{}, err
	}

	return stored, info, nil
}

func orUnknownType(mimeType string) string {
	if mimeType == "" {
		return "unknown type"
	}

	return mimeType
}

// imageMimes is what a file extension says about an image Slack did not describe.
var imageMimes = map[string]string{
	"png": "image/png", "jpg": "image/jpeg", "jpeg": "image/jpeg", "gif": "image/gif",
	"webp": "image/webp", "bmp": "image/bmp", "heic": "image/heic",
}

// imageMime answers the type of a file, preferring what Slack said and falling back to
// the extension of a name from the archive.
func imageMime(name, mimetype string) string {
	if mimetype != "" {
		return mimetype
	}

	extension := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))

	if candidate, known := imageMimes[extension]; known {
		return candidate
	}

	return ""
}

func postMessage(deps Deps) Handler {
	return func(_ context.Context, raw json.RawMessage) ([]Content, error) {
		var args struct {
			Workspace   string   `json:"workspace"`
			Channel     string   `json:"channel"`
			Text        string   `json:"text"`
			ThreadTS    string   `json:"thread_ts"`
			Confirm     bool     `json:"confirm"`
			Assist      *bool    `json:"assist"`
			Attachments []string `json:"attachments"`
		}

		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}

		planned, err := slack.PlanAttachments(args.Attachments)
		if err != nil {
			return nil, failureText(err)
		}

		session, target, _, label, where, thread, err := preparePost(deps, args.Workspace, args.Channel, args.ThreadTS)
		if err != nil {
			return nil, failureText(err)
		}

		signed, err := signedText(deps, session, args.Text, args.Assist)
		if err != nil {
			return nil, failureText(err)
		}

		var listed strings.Builder

		files := make([]string, 0, len(planned))

		for _, plan := range planned {
			listed.WriteString("\n  " + slack.DescribeAttachment(plan))
			files = append(files, plan.Name+" ("+slack.HumanSize(plan.Bytes)+")")
		}

		attached := ""

		if listed.Len() > 0 {
			attached = "\n\nAttached:" + listed.String()
		}

		if !args.Confirm {
			return Text("Nothing sent. Would post to " + where + thread + ":\n\n" + signed.Text +
				attached + "\n\nCall again with confirm=true to send it."), nil
		}

		if len(planned) > 0 {
			return sendWithAttachments(deps, session, target, label, where, thread, signed, planned, files, args.ThreadTS)
		}

		var blocks any
		if len(signed.Blocks) > 0 {
			blocks = signed.Blocks
		}

		posted, err := session.client.PostMessage(target.ID, signed.Text, args.ThreadTS, blocks)
		if err != nil {
			return nil, failureText(err)
		}

		audit, err := slack.RecordSend(deps.Paths.AuditLog, slack.SendRecord{
			Workspace: session.account.TeamName, Channel: label, ChannelID: target.ID,
			ThreadTS: args.ThreadTS, MessageTS: posted.TS, Text: signed.Text,
		}, time.Now())
		if err != nil {
			return nil, failureText(err)
		}

		return Text("Sent to " + where + thread + " at ts " + posted.TS + ". Audit: " + audit), nil
	}
}

// signedText resolves what signs the message: the environment over the settings file
// over the generated default, and off for this send when assist is false.
func signedText(deps Deps, session session, text string, assist *bool) (slack.Signed, error) {
	settings, err := config.NewStore(deps.Paths.SettingsFile).Read()
	if err != nil {
		return slack.Signed{}, err
	}

	signing, err := config.SigningFor(settings, deps.Env)
	if err != nil {
		return slack.Signed{}, err
	}

	wanted := assist == nil || *assist
	nickname := config.ResolveNickname(signing, forumAgentName(deps), session.account.User)

	if !wanted || nickname == nil {
		return slack.Signed{Text: text}, nil
	}

	return slack.SignedMessage(text, *nickname)
}

// preparePost answers where a message would go, without a network round trip: a
// write reads the directory it already has.
func preparePost(deps Deps, workspace, channel, threadTS string) (session, slack.Channel, *slack.Directory, string, string, string, error) {
	session, err := openSession(deps, workspace)
	if err != nil {
		return session, slack.Channel{}, nil, "", "", "", err
	}

	directory, err := slack.NewDirectoryStore(deps.Paths.ArchiveDir).Read(session.account.TeamID)
	if err != nil {
		directory = nil
	}

	target, err := session.client.ResolveChannel(channel, func(name string) (string, bool) {
		user := directory.UserByReference(name)
		if user == nil {
			return "", false
		}

		return user.ID, true
	})
	if err != nil {
		return session, slack.Channel{}, nil, "", "", "", err
	}

	label := slack.ChannelLabel(target, directory)
	where := session.account.TeamName + " · " + label + " (" + target.ID + ")"

	thread := ""
	if threadTS != "" {
		thread = " in thread " + threadTS
	}

	return session, target, directory, label, where, thread, nil
}

func sendWithAttachments(
	deps Deps,
	session session,
	target slack.Channel,
	label, where, thread string,
	signed slack.Signed,
	planned []slack.Plan,
	files []string,
	threadTS string,
) ([]Content, error) {
	uploads, err := slack.ReadAttachments(planned)
	if err != nil {
		return nil, failureText(err)
	}

	uploaded, err := session.client.UploadFiles(target.ID, uploads, signed.Text, threadTS)
	if err != nil {
		return nil, failureText(err)
	}

	audit, err := slack.RecordSend(deps.Paths.AuditLog, slack.SendRecord{
		Workspace: session.account.TeamName, Channel: label, ChannelID: target.ID,
		ThreadTS: threadTS, MessageTS: uploaded.TS, Text: signed.Text, Files: files,
	}, time.Now())
	if err != nil {
		return nil, failureText(err)
	}

	ts := "(Slack reported no ts)"
	if uploaded.TS != "" {
		ts = "at ts " + uploaded.TS
	}

	ids := ""
	if len(uploaded.FileIDs) > 0 {
		ids = " (" + strings.Join(uploaded.FileIDs, ", ") + ")"
	}

	return Text("Sent to " + where + thread + " " + ts + " with " + strconv.Itoa(len(planned)) +
		" attachment(s)" + ids + ". Audit: " + audit), nil
}
