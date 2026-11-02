package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/archive"
	"github.com/cruizba/slack-browser-mcp/internal/auth"
	"github.com/cruizba/slack-browser-mcp/internal/config"
	"github.com/cruizba/slack-browser-mcp/internal/failure"
	"github.com/cruizba/slack-browser-mcp/internal/platform"
	"github.com/cruizba/slack-browser-mcp/internal/redact"
	"github.com/cruizba/slack-browser-mcp/internal/slack"
)

// Deps is what a tool needs from the process: where the files are, what the
// environment says, and the binary to spawn a login with.
type Deps struct {
	Paths   config.Paths
	Env     platform.Env
	Version string
	// Self is the executable, used by `login_workspace` to start the CLI in the
	// background instead of driving a browser from inside the server.
	Self string
	// BaseURL points the client at another Slack, which is what the harness and the
	// tests need; empty means the real one.
	BaseURL string
}

const (
	// maxInlineImageBytes is where an image stops being worth a third more bytes in
	// base64: the model pays for every one of them.
	maxInlineImageBytes = 4 * 1024 * 1024
	// nudgeCooldown keeps a failing sync from being restarted by every read.
	nudgeCooldown = 10 * time.Minute
)

// Handlers is the slack surface: status, workspaces, reads, one write.
func Handlers(deps Deps) map[string]Handler {
	return map[string]Handler{
		"auth_status":           authStatus(deps),
		"login_workspace":       loginWorkspace(deps),
		"list_workspaces":       listWorkspaces(deps),
		"set_default_workspace": setDefaultWorkspace(deps),
		"set_assisted_nickname": setAssistedNickname(deps),
		"list_conversations":    listConversations(deps),
		"read_messages":         readMessages(deps),
		"get_thread":            getThread(deps),
		"read_file":             readFile(deps),
		"post_message":          postMessage(deps),
		"search_messages":       searchMessages(deps),
		"archive_messages":      archiveMessages(deps),
		"archive_status":        archiveStatus(deps),
	}
}

// failureText is how a failed tool answers: the reason, redacted, plus the next step
// when the error type carries one. It is a result and not a protocol error, because
// the agent has to read the reason.
func failureText(err error) error {
	return errors.New(redact.Text(hintOf(err)))
}

func hintOf(err error) string {
	message := redact.Text(err.Error())

	if authErr, ok := errors.AsType[*failure.AuthError](err); ok {
		return message + "\n" + authErr.Hint
	}

	if schemaErr, ok := errors.AsType[*failure.SchemaError](err); ok {
		return message + "\n" + schemaErr.Hint
	}

	if forumErr, ok := errors.AsType[*failure.ForumError](err); ok && forumErr.Hint != "" {
		return message + "\n" + forumErr.Hint
	}

	return message
}

// session is the account a call runs as, and the client that talks for it.
type session struct {
	account auth.Account
	client  *slack.Client
}

func openSession(deps Deps, reference string) (session, error) {
	store := newAuthStore(deps)

	account, err := store.Resolve(reference)
	if err != nil {
		return session{}, err
	}

	return session{account: account, client: newClient(deps, account)}, nil
}

// newClient talks for one workspace, against the base the caller resolved rather than
// whatever the process environment happens to hold.
func newClient(deps Deps, account auth.Account) *slack.Client {
	options := slack.Options{BaseURL: deps.BaseURL}

	if raw, present := platform.Lookup(deps.Env, "SLACK_BROWSER_PACE_MS"); present {
		if milliseconds, err := strconv.ParseFloat(strings.TrimSpace(raw), 64); err == nil && milliseconds >= 0 {
			pace := time.Duration(milliseconds * float64(time.Millisecond))
			options.Pace = &pace
		}
	}

	return slack.NewClient(account, options)
}

func newAuthStore(deps Deps) *auth.Store {
	return auth.NewStore(deps.Paths.Accounts, config.NewStore(deps.Paths.SettingsFile), deps.Env)
}

// callDirectory is the directory for one call, fetched only if the call needs a
// name: a read that resolves a channel by id costs no lookup.
func callDirectory(deps Deps, account auth.Account, client *slack.Client) *slack.Memoized {
	store := slack.NewDirectoryStore(deps.Paths.ArchiveDir)

	return slack.NewMemoized(func() (*slack.Directory, error) {
		return slack.LoadDirectory(slack.LoadOptions{Client: client, TeamID: account.TeamID, Store: store})
	})
}

func authStatus(deps Deps) Handler {
	return func(_ context.Context, raw json.RawMessage) ([]Content, error) {
		var args struct {
			Workspace string `json:"workspace"`
		}

		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}

		store := newAuthStore(deps)

		summaries, err := store.List()
		if err != nil {
			return nil, failureText(err)
		}

		targets := make([]string, 0, len(summaries))

		if args.Workspace != "" {
			account, err := store.Resolve(args.Workspace)
			if err != nil {
				return nil, failureText(err)
			}

			targets = append(targets, account.TeamID)
		} else {
			for _, summary := range summaries {
				targets = append(targets, summary.TeamID)
			}
		}

		if len(targets) == 0 {
			return Text("No Slack session stored. Call login_workspace: it opens Chrome so the user can sign in.\n" +
				loginSummary(deps)), nil
		}

		lines := make([]string, 0, len(targets)+1)

		for _, teamID := range targets {
			account, err := store.Get(teamID)
			if err != nil {
				return nil, failureText(err)
			}

			info, err := newClient(deps, account).AuthTest()
			if err != nil {
				lines = append(lines, "FAIL "+account.TeamName+" ("+account.TeamID+"): "+
					redact.Text(err.Error())+" — call login_workspace to sign in again")

				continue
			}

			_ = store.MarkValidated(account.TeamID, time.Now())

			lines = append(lines, "ok  "+info.Team+" ("+info.TeamID+") as "+info.User+" ["+info.UserID+"]")
		}

		if args.Workspace == "" {
			lines = append(lines, defaultLine(store, summaries))
		}

		return Text(strings.Join(lines, "\n")), nil
	}
}

// defaultLine says which workspace answers a call that names none, and why.
func defaultLine(store *auth.Store, summaries []auth.Summary) string {
	fallback, err := store.Default()
	if err != nil {
		return "default: FAIL " + redact.Text(err.Error())
	}

	if fallback == nil {
		return "default: none — pass workspace on every call"
	}

	name := fallback.TeamID

	for _, summary := range summaries {
		if summary.TeamID == fallback.TeamID {
			name = summary.TeamName

			break
		}
	}

	source := "the only stored session"

	switch fallback.Source {
	case auth.SourceEnvironment:
		source = "SLACK_BROWSER_MCP_TEAM"
	case auth.SourceConfig:
		source = "config.json"
	case auth.SourceOnly:
		source = "the only stored session"
	}

	return "default: " + name + " (" + fallback.TeamID + ") via " + source
}

// workspaceLine is one stored workspace as the listing prints it.
func workspaceLine(account auth.Summary, fallback *auth.Default) string {
	marker := " "

	if fallback != nil && fallback.TeamID == account.TeamID {
		marker = "*"
	}

	bits := []string{account.TeamName + " (" + account.TeamID + ")"}

	if account.TeamDomain != "" {
		bits = append(bits, account.TeamDomain)
	}

	if account.User != "" {
		bits = append(bits, "as "+account.User)
	}

	if account.LastOKAt != "" {
		bits = append(bits, "last ok "+account.LastOKAt)
	} else {
		bits = append(bits, "never validated")
	}

	return marker + " " + strings.Join(bits, " · ")
}

// loginSummary says whether a sign-in is running and how the last one ended.
func loginSummary(deps Deps) string {
	lock := archive.NewLock(deps.Paths.LoginLock)

	owner, err := lock.Owner()
	if err == nil && owner != nil {
		return fmt.Sprintf("login: running since %s (pid %d)",
			owner.StartedAt.UTC().Format("2006-01-02T15:04:05.000Z"), owner.PID)
	}

	if lines := tailLines(deps.Paths.LoginLog, 1); len(lines) > 0 {
		return "login: idle; last attempt: " + lines[0]
	}

	return "login: idle"
}

// tailLines is the end of a log, without the runtime's own warnings: those are not
// an outcome.
func tailLines(file string, count int) []string {
	raw, err := os.ReadFile(file) //nolint:gosec // the path is the login log from config
	if err != nil {
		return nil
	}

	lines := make([]string, 0, count)

	for line := range strings.SplitSeq(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "(node:") || strings.HasPrefix(trimmed, "(Use `node") {
			continue
		}

		lines = append(lines, trimmed)
	}

	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}

	return lines
}

func loginWorkspace(deps Deps) Handler {
	return func(_ context.Context, raw json.RawMessage) ([]Content, error) {
		var args struct {
			Workspace   string `json:"workspace"`
			WaitSeconds int    `json:"wait_seconds"`
		}

		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}

		if args.WaitSeconds == 0 {
			args.WaitSeconds = 20
		}

		lock := archive.NewLock(deps.Paths.LoginLock)

		if owner, _ := lock.Owner(); owner != nil {
			return Text(fmt.Sprintf("A browser sign-in is already running (pid %d, started %s). "+
				"Finish it in the window that is open, then call auth_status.",
				owner.PID, owner.StartedAt.UTC().Format("2006-01-02T15:04:05.000Z"))), nil
		}

		store := newAuthStore(deps)

		known := map[string]bool{}

		summaries, err := store.List()
		if err != nil {
			return nil, failureText(err)
		}

		for _, summary := range summaries {
			known[summary.TeamID] = true
		}

		if err := startLogin(deps); err != nil {
			return nil, failureText(err)
		}

		if !waitForLock(lock, 3*time.Second) {
			return nil, failureText(loginBroke(deps, "the browser did not start"))
		}

		// The lock can exist for a moment before Chrome refuses to run, and reporting
		// "the browser is open" then would send the user to a window that is not there.
		time.Sleep(1200 * time.Millisecond)

		if owner, _ := lock.Owner(); owner == nil {
			return nil, failureText(loginBroke(deps, "the browser closed during start-up"))
		}

		if signed, done := awaitSignIn(deps, store, lock, known, time.Duration(args.WaitSeconds)*time.Second); done != nil {
			return signed, done
		}

		hint := ""
		if args.Workspace != "" {
			hint = ": sign in to " + args.Workspace
		}

		return Text("Chrome is open on app.slack.com" + hint + ". " +
			"Finish the sign-in there; nothing is stored until Slack accepts the session. " +
			"Then call auth_status or list_workspaces. Log: " + deps.Paths.LoginLog), nil
	}
}

// awaitSignIn watches for the session the browser is about to leave behind, and
// answers nil, nil while the window is still open and worth waiting on.
func awaitSignIn(
	deps Deps,
	store *auth.Store,
	lock *archive.Lock,
	known map[string]bool,
	wait time.Duration,
) ([]Content, error) {
	deadline := time.Now().Add(wait)

	for time.Now().Before(deadline) {
		if added := addedSince(store, known); len(added) > 0 {
			return Text(signedIn(deps, store, added)), nil
		}

		if owner, _ := lock.Owner(); owner == nil {
			break
		}

		time.Sleep(500 * time.Millisecond)
	}

	if added := addedSince(store, known); len(added) > 0 {
		return Text(signedIn(deps, store, added)), nil
	}

	if owner, _ := lock.Owner(); owner == nil {
		return nil, failureText(loginBroke(deps, "the sign-in did not finish"))
	}

	return nil, nil
}

// startLogin runs the CLI's own login as a detached child, so the browser outlives
// the call that opened it.
func startLogin(deps Deps) error {
	if deps.Self == "" {
		return errors.New("the slack-browser binary is not known to this server, so the browser cannot be started. " +
			"Run `slack-browser login` in a terminal instead.")
	}

	if err := os.MkdirAll(filepath.Dir(deps.Paths.LoginLog), 0o700); err != nil {
		return err
	}

	log, err := os.OpenFile(deps.Paths.LoginLog, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}

	defer func() { _ = log.Close() }()

	command := exec.CommandContext(context.Background(), deps.Self, "login") //nolint:gosec // the binary is this one
	command.Stdout = log
	command.Stderr = log
	command.Stdin = nil

	if err := command.Start(); err != nil {
		return err
	}

	return command.Process.Release()
}

func waitForLock(lock *archive.Lock, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if owner, _ := lock.Owner(); owner != nil {
			return true
		}

		time.Sleep(100 * time.Millisecond)
	}

	return false
}

func loginBroke(deps Deps, what string) error {
	tail := strings.Join(tailLines(deps.Paths.LoginLog, 6), "\n")

	if tail != "" {
		return errors.New(what + ":\n" + tail)
	}

	return errors.New(what + "; see " + deps.Paths.LoginLog)
}

func addedSince(store *auth.Store, known map[string]bool) []auth.Summary {
	summaries, err := store.List()
	if err != nil {
		return nil
	}

	added := make([]auth.Summary, 0, 1)

	for _, summary := range summaries {
		if !known[summary.TeamID] {
			added = append(added, summary)
		}
	}

	return added
}

func signedIn(deps Deps, store *auth.Store, added []auth.Summary) string {
	names := make([]string, 0, len(added))

	for _, account := range added {
		line := account.TeamName + " (" + account.TeamID + ")"

		if account.User != "" {
			line += " as " + account.User
		}

		names = append(names, line)
	}

	isDefault := false

	if fallback, err := store.Default(); err == nil && fallback != nil {
		for _, account := range added {
			if account.TeamID == fallback.TeamID {
				isDefault = true
			}
		}
	}

	note := ""
	if isDefault {
		note = " — it is the default workspace"
	}

	return "signed in: " + strings.Join(names, ", ") + note + "\n" + loginSummary(deps)
}

func listWorkspaces(deps Deps) Handler {
	return func(_ context.Context, _ json.RawMessage) ([]Content, error) {
		store := newAuthStore(deps)

		accounts, err := store.List()
		if err != nil {
			return nil, failureText(err)
		}

		summary := loginSummary(deps)

		if len(accounts) == 0 {
			return Text("No Slack workspace stored yet. Call login_workspace to open a browser and sign in to one.\n" +
				summary), nil
		}

		fallback, err := store.Default()
		if err != nil {
			return nil, failureText(err)
		}

		source := ""

		if fallback != nil {
			switch fallback.Source {
			case auth.SourceConfig:
				source = " from config.json"
			case auth.SourceEnvironment:
				source = " from SLACK_BROWSER_MCP_TEAM"
			case auth.SourceOnly:
				source = ": the only stored session"
			}
		}

		lines := make([]string, 0, len(accounts)+2)

		for _, account := range accounts {
			lines = append(lines, workspaceLine(account, fallback))
		}

		count := "stored: " + strconv.Itoa(len(accounts)) + " workspaces"
		if len(accounts) == 1 {
			count = "stored: 1 workspace"
		}

		if fallback != nil {
			count += "; * = default" + source
		} else {
			count += "; no default, pass workspace on every call"
		}

		lines = append(lines, count, summary)

		return Text(strings.Join(lines, "\n")), nil
	}
}

func setDefaultWorkspace(deps Deps) Handler {
	return func(_ context.Context, raw json.RawMessage) ([]Content, error) {
		var args struct {
			Workspace string `json:"workspace"`
			Clear     bool   `json:"clear"`
		}

		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}

		if args.Clear && args.Workspace != "" {
			return nil, errors.New("Pass either workspace or clear, not both.")
		}

		store := newAuthStore(deps)
		settings := config.NewStore(deps.Paths.SettingsFile)

		accounts, err := store.List()
		if err != nil {
			return nil, failureText(err)
		}

		if args.Clear {
			if _, err := settings.SetDefaultWorkspace(nil); err != nil {
				return nil, failureText(err)
			}

			if len(accounts) > 1 {
				return Text("Default workspace cleared; every call must name a workspace now."), nil
			}

			return Text("Default workspace cleared; with a single stored session it is still used implicitly."), nil
		}

		if args.Workspace == "" {
			return nil, errors.New("Pass workspace (name, domain or team id), or clear: true.")
		}

		account, err := store.Resolve(args.Workspace)
		if err != nil {
			return nil, failureText(err)
		}

		reference := account.TeamID

		if _, err := settings.SetDefaultWorkspace(&reference); err != nil {
			return nil, failureText(err)
		}

		return Text("default workspace: " + account.TeamName + " (" + account.TeamID +
			"); calls without a workspace now use it."), nil
	}
}

func setAssistedNickname(deps Deps) Handler {
	return func(_ context.Context, raw json.RawMessage) ([]Content, error) {
		var args struct {
			Nickname string `json:"nickname"`
			Clear    bool   `json:"clear"`
		}

		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}

		if args.Clear && args.Nickname != "" {
			return nil, errors.New("Pass either nickname or clear, not both.")
		}

		settings := config.NewStore(deps.Paths.SettingsFile)

		if args.Clear {
			if _, err := settings.SetAssistedNickname(nil); err != nil {
				return nil, failureText(err)
			}

			current, err := settings.Read()
			if err != nil {
				return nil, failureText(err)
			}

			signing, err := config.SigningFor(current, deps.Env)
			if err != nil {
				return nil, failureText(err)
			}

			return Text("assisted nickname cleared; " + slack.DescribeSigning(signing, agentName(deps))), nil
		}

		if args.Nickname != "" {
			nickname := args.Nickname

			if _, err := settings.SetAssistedNickname(&nickname); err != nil {
				return nil, failureText(err)
			}

			trimmed := strings.TrimSpace(nickname)

			return Text("assisted nickname: " + trimmed + "; every message now opens with *" +
				slack.AssistedHeader + " " + trimmed + "* between two rule lines"), nil
		}

		current, err := settings.Read()
		if err != nil {
			return nil, failureText(err)
		}

		signing, err := config.SigningFor(current, deps.Env)
		if err != nil {
			return nil, failureText(err)
		}

		return Text(slack.DescribeSigning(signing, agentName(deps))), nil
	}
}

// agentName is the nickname the generated signature uses: the client behind the
// call, which the identity layer already knows how to read.
func agentName(deps Deps) string {
	return forumAgentName(deps)
}

func listConversations(deps Deps) Handler {
	return func(_ context.Context, raw json.RawMessage) ([]Content, error) {
		var args struct {
			Workspace string `json:"workspace"`
			Query     string `json:"query"`
			Kind      string `json:"kind"`
			Limit     int    `json:"limit"`
		}

		if err := decodeArgs(raw, &args); err != nil {
			return nil, err
		}

		if args.Kind == "" {
			args.Kind = "all"
		}

		if args.Limit == 0 {
			args.Limit = 100
		}

		session, err := openSession(deps, args.Workspace)
		if err != nil {
			return nil, failureText(err)
		}

		directory := callDirectory(deps, session.account, session.client).Get()

		lines := conversationLines(session, directory, args.Kind, args.Query, args.Limit)
		if lines.shown == 0 {
			return Text("No conversation in " + session.account.TeamName + " matches that filter."), nil
		}

		footer := ""
		if lines.overflow > 0 {
			footer = "\n\nshowing " + strconv.Itoa(lines.shown) + " of " + strconv.Itoa(lines.total) +
				"; raise limit or narrow query"
		}

		return Text(session.account.TeamName + " — conversations\n" +
			strings.Join(lines.lines, "\n") + footer), nil
	}
}

// conversationEntry is one conversation as a listing names it.
type conversationEntry struct {
	id      string
	label   string
	members int
}

// conversationList is the page a listing answered with, and how much it left out.
type conversationList struct {
	lines    []string
	shown    int
	total    int
	overflow int
}

// conversationLines labels and filters the conversations, from the directory when it
// has them and from Slack otherwise.
func conversationLines(session session, directory *slack.Directory, kind, query string, limit int) conversationList {
	entries := make([]conversationEntry, 0, 64)

	entries = append(entries, listedConversations(session, directory, kind)...)

	// A DM has no name, so filtering and sorting on the label is what lets a query find
	// a person at all.
	sort.SliceStable(entries, func(left, right int) bool {
		return entries[left].label < entries[right].label
	})

	needle := strings.ToLower(query)

	matching := make([]conversationEntry, 0, len(entries))

	for _, candidate := range entries {
		if needle != "" && !strings.Contains(strings.ToLower(candidate.label), needle) {
			continue
		}

		matching = append(matching, candidate)
	}

	shown := matching
	if len(shown) > limit {
		shown = shown[:limit]
	}

	lines := make([]string, 0, len(shown))

	for _, item := range shown {
		line := item.label + " (" + item.id + ")"

		if item.members > 0 {
			line += " " + strconv.Itoa(item.members) + " members"
		}

		lines = append(lines, line)
	}

	return conversationList{lines: lines, shown: len(shown), total: len(matching), overflow: len(matching) - len(shown)}
}

// listedConversations answers the conversations a listing can see, from the
// directory when it has them and from Slack otherwise.
func listedConversations(session session, directory *slack.Directory, kind string) []conversationEntry {
	listed := make([]conversationEntry, 0, 64)

	if directory != nil && len(directory.Channels) > 0 {
		for _, channel := range directory.Channels {
			if !wantedKind(kind, channel.Kind) {
				continue
			}

			listed = append(listed, conversationEntry{
				id: channel.ID,
				label: slack.ConversationLabel(slack.ConversationRef{
					ID: channel.ID, Name: channel.Name, Peer: channel.User, Kind: channel.Kind,
				}, directory),
				members: channel.NumMembers,
			})
		}

		return listed
	}

	channels, err := session.client.ListMyChannels()
	if err != nil {
		return listed
	}

	for _, channel := range channels {
		if !wantedChannelKind(kind, channel) {
			continue
		}

		listed = append(listed, conversationEntry{
			id: channel.ID, label: slack.ChannelLabel(channel, directory), members: channel.NumMembers,
		})
	}

	return listed
}

func wantedKind(kind string, actual slack.Kind) bool {
	switch kind {
	case "channel":
		return actual == slack.KindChannel
	case "private":
		return actual == slack.KindPrivate
	case "dm":
		return actual == slack.KindDM || actual == slack.KindMpIM
	default:
		return true
	}
}

func wantedChannelKind(kind string, channel slack.Channel) bool {
	switch kind {
	case "channel":
		return !channel.IsPrivate && !channel.IsIM && !channel.IsMpIM
	case "private":
		return channel.IsPrivate
	case "dm":
		return channel.IsIM || channel.IsMpIM
	default:
		return true
	}
}

func decodeArgs(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return nil
	}

	if err := json.Unmarshal(raw, target); err != nil {
		return errors.New("the arguments of this call could not be read: " + err.Error())
	}

	return nil
}

// wantsRefresh folds a millisecond age into the policy's own unit.
func wantsRefresh(ageMS *int64, atNow bool, policy config.FreshnessPolicy) bool {
	if ageMS == nil {
		return config.WantsRefresh(nil, atNow, policy)
	}

	age := time.Duration(*ageMS) * time.Millisecond

	return config.WantsRefresh(&age, atNow, policy)
}

// formatAge is how fresh an answer is, in the words both surfaces print.
func formatAge(age *int64) string {
	return archive.FormatAge(age)
}

// base64Image is an image the tool answers with, which the model can look at.
func base64Image(path, mimeType string) ([]Content, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the path is the archive's own store
	if err != nil {
		return nil, err
	}

	return []Content{{
		Type:     "image",
		Data:     base64.StdEncoding.EncodeToString(raw),
		MimeType: mimeType,
	}}, nil
}
