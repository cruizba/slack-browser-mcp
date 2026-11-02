package cli

import (
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/auth"
	"github.com/cruizba/slack-browser-mcp/internal/buildinfo"
	"github.com/cruizba/slack-browser-mcp/internal/config"
	"github.com/cruizba/slack-browser-mcp/internal/failure"
	"github.com/cruizba/slack-browser-mcp/internal/platform"
	"github.com/cruizba/slack-browser-mcp/internal/slack"
)

const (
	cmdAccounts  = "accounts"
	cmdWorkspace = "workspace"
	cmdVersion   = "version"

	cmdThread = "thread"
	cmdStatus = "status"
	cmdRead   = "read"

	subList  = "list"
	subClear = "clear"
)

// Context is everything a command may touch: where the files are, what the
// environment says, and the two streams. Passing them in is what lets a test run
// a command without a process, and what keeps a command from reading a path the
// rest of the call disagrees with.
type Context struct {
	Env    platform.Env
	Paths  config.Paths
	Stdout io.Writer
	Stderr io.Writer
}

// Run dispatches one invocation and returns the exit code.
func Run(args []string, ctx Context) int {
	command := "help"

	var tokens []string

	if len(args) > 0 {
		command = args[0]
		tokens = args[1:]
	}

	name, known := resolveCommand(command)

	if !known {
		return unknown(command, tokens, ctx)
	}

	if page, ok := helpPageFor(command, tokens); ok {
		emit(ctx.Stdout, "%s\n", page)

		return 0
	}

	parsed, err := parseArgs(tokens, flagSpecs(name), name)
	if err != nil {
		return fail(ctx, err)
	}

	subcommand := ""
	if len(parsed.Positional) > 0 {
		subcommand = parsed.Positional[0]
	} else {
		subcommand = defaultSubcommand(name)
	}

	if problem := usageProblem(command, subcommand, parsed.Flags()); problem != "" {
		emit(ctx.Stderr, "%s\n", problem)

		return 1
	}

	switch name {
	case cmdAccounts:
		return commandAccounts(ctx)
	case cmdWorkspace:
		return commandWorkspace(ctx, parsed)
	case cmdVersion:
		emit(ctx.Stdout, "%s %s\n", program, buildinfo.Line())

		return 0
	case cmdStatus:
		return commandStatus(ctx, parsed)
	case cmdRead:
		return commandRead(ctx, parsed)
	case cmdThread:
		return commandThread(ctx, parsed)
	default:
		emit(ctx.Stderr, "%s %s is not ported yet.\n", program, name)

		return 1
	}
}

// unknown handles the three spellings of the index page and the commands nobody
// has, which are the only two ways to get here.
func unknown(command string, tokens []string, ctx Context) int {
	if command == "help" || command == "--help" || command == "-h" {
		topic := ""

		for _, token := range tokens {
			if !strings.HasPrefix(token, "-") {
				topic = token

				break
			}
		}

		if topic == "" {
			emit(ctx.Stdout, "%s\n", indexPage())

			return 0
		}

		page, ok := commandPage(topic, "")
		if !ok {
			emit(ctx.Stderr, "no command named `%s`\n\n%s\n", topic, indexPage())

			return 1
		}

		emit(ctx.Stdout, "%s\n", page)

		return 0
	}

	emit(ctx.Stderr, "Unknown command: %s\n\n", command)
	emit(ctx.Stderr, "%s\n", indexPage())

	return 1
}

func commandAccounts(ctx Context) int {
	accounts, fallback, code := storedAccounts(ctx)
	if code != 0 {
		return code
	}

	if len(accounts) == 0 {
		emit(ctx.Stdout, "%s\n", "No stored session. Run `slack-browser login`.")

		return 0
	}

	for _, account := range accounts {
		lastOK := "never validated"
		if account.LastOKAt != "" {
			lastOK = "last ok " + account.LastOKAt
		}

		marker := "  "
		if fallback != nil && fallback.TeamID == account.TeamID {
			marker = "* "
		}

		emit(ctx.Stdout, "%s%s  %s  %s  %s  %s\n",
			marker, account.TeamID, account.TeamName, orDash(account.User), orDash(account.TeamDomain), lastOK)
	}

	emit(ctx.Stdout, "%s\n", describeDefault(fallback, accounts))

	return 0
}

func commandWorkspace(ctx Context, parsed ParsedArgs) int {
	subcommand := subList
	if len(parsed.Positional) > 0 {
		subcommand = parsed.Positional[0]
	}

	switch subcommand {
	case subList:
		return commandAccounts(ctx)
	case "use":
		if len(parsed.Positional) < 2 || parsed.Positional[1] == "" {
			return fail(ctx, errors.New("workspace use needs a name, domain or team id"))
		}

		store := newStore(ctx)

		account, err := store.Resolve(parsed.Positional[1])
		if err != nil {
			return fail(ctx, err)
		}

		if _, err := config.NewStore(ctx.Paths.SettingsFile).SetDefaultWorkspace(&account.TeamID); err != nil {
			return fail(ctx, err)
		}

		emit(ctx.Stdout, "default workspace: %s (%s)\n", account.TeamName, account.TeamID)

		return 0
	case subClear:
		if _, err := config.NewStore(ctx.Paths.SettingsFile).SetDefaultWorkspace(nil); err != nil {
			return fail(ctx, err)
		}

		accounts, err := newStore(ctx).List()
		if err != nil {
			return fail(ctx, err)
		}

		if len(accounts) > 1 {
			emit(ctx.Stdout, "%s\n", "default workspace cleared; pass --workspace on every call that needs one")
		} else {
			emit(ctx.Stdout, "%s\n", "default workspace cleared; the only stored session is still used implicitly")
		}

		return 0
	default:
		return fail(ctx, errors.New("unknown workspace subcommand: "+subcommand))
	}
}

// storedAccounts reads the sessions and the default, reporting a broken default
// on stderr instead of failing: the list is still worth printing without it.
func storedAccounts(ctx Context) ([]auth.Summary, *auth.Default, int) {
	store := newStore(ctx)

	accounts, err := store.List()
	if err != nil {
		return nil, nil, fail(ctx, err)
	}

	fallback, err := store.Default()
	if err != nil {
		emit(ctx.Stderr, "default workspace is broken: %s\n", err)

		fallback = nil
	}

	return accounts, fallback, 0
}

var defaultSourceText = map[auth.DefaultSource]string{
	auth.SourceEnvironment: auth.TeamEnv,
	auth.SourceConfig:      "config.json",
	auth.SourceOnly:        "the only stored session",
}

func describeDefault(fallback *auth.Default, accounts []auth.Summary) string {
	if fallback == nil {
		return "no default workspace: pass --workspace, or run " +
			"`slack-browser workspace use <name|domain|team-id>`"
	}

	name := fallback.TeamID

	for _, account := range accounts {
		if account.TeamID == fallback.TeamID {
			name = account.TeamName

			break
		}
	}

	return fmt.Sprintf("default workspace: %s (%s) via %s", name, fallback.TeamID, defaultSourceText[fallback.Source])
}

func newStore(ctx Context) *auth.Store {
	return auth.NewStore(ctx.Paths.Accounts, config.NewStore(ctx.Paths.SettingsFile), ctx.Env)
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}

	return value
}

// emit writes to one of the streams. A failed write to a terminal or a closed
// pipe cannot be acted on — the run is over either way — so the error is dropped
// here once instead of at every call site.
func emit(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

// fail maps the error to the exit code the command line has always used: a
// session Slack refused and an archive layout this build does not know both exit
// 2, because both are fixed by doing something outside the command and trying
// again; everything else is a plain failure.
func fail(ctx Context, err error) int {
	if authErr, ok := errors.AsType[*failure.AuthError](err); ok {
		emit(ctx.Stderr, "%s\n%s\n", authErr.Message, authErr.Hint)

		return 2
	}

	if schemaErr, ok := errors.AsType[*failure.SchemaError](err); ok {
		emit(ctx.Stderr, "%s\n%s\n", schemaErr.Message, schemaErr.Hint)

		return 2
	}

	if forumErr, ok := errors.AsType[*failure.ForumError](err); ok {
		if forumErr.Hint != "" {
			emit(ctx.Stderr, "%s\n%s\n", forumErr.Message, forumErr.Hint)
		} else {
			emit(ctx.Stderr, "%s\n", forumErr.Message)
		}

		return 1
	}

	emit(ctx.Stderr, "%s\n", err.Error())

	return 1
}

// numberFlag reads a flag that expects a positive number, refusing the rest
// with what it found rather than a zero nobody asked for.
func numberFlag(parsed ParsedArgs, name string, fallback int) (int, error) {
	raw, ok := parsed.String(name)
	if !ok {
		return fallback, nil
	}

	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0, errors.New("--" + name + " expects a positive number, got \"" + raw + "\"")
	}

	return int(value), nil
}

func newClient(account auth.Account) *slack.Client {
	return slack.NewClient(account, slack.Options{})
}

func commandStatus(ctx Context, parsed ParsedArgs) int {
	store := newStore(ctx)

	reference, hasReference := parsed.String("workspace")

	var accounts []auth.Account

	if hasReference {
		account, err := store.Resolve(reference)
		if err != nil {
			return fail(ctx, err)
		}

		accounts = []auth.Account{account}
	} else {
		summaries, err := store.List()
		if err != nil {
			return fail(ctx, err)
		}

		for _, summary := range summaries {
			account, err := store.Get(summary.TeamID)
			if err != nil {
				return fail(ctx, err)
			}

			accounts = append(accounts, account)
		}
	}

	if len(accounts) == 0 {
		emit(ctx.Stdout, "No stored session. Run `slack-browser login`.\n")

		return 0
	}

	failures := 0

	for _, account := range accounts {
		info, err := newClient(account).AuthTest()
		if err != nil {
			failures++

			emit(ctx.Stderr, "FAIL  %s (%s): %s\n", account.TeamName, account.TeamID, err.Error())

			continue
		}

		if err := store.MarkValidated(account.TeamID, time.Now()); err != nil {
			return fail(ctx, err)
		}

		emit(ctx.Stdout, "ok    %s (%s) as %s\n", info.Team, info.TeamID, info.User)
	}

	if failures > 0 {
		return 2
	}

	return 0
}

// loadDirectory is the memoized, disk-backed directory: naming a person is the
// only thing that needs it, and most calls name a channel.
func loadDirectory(ctx Context, client *slack.Client, teamID string) *slack.Memoized {
	store := slack.NewDirectoryStore(ctx.Paths.ArchiveDir)

	return slack.NewMemoized(func() (*slack.Directory, error) {
		return slack.LoadDirectory(slack.LoadOptions{Client: client, TeamID: teamID, Store: store})
	})
}

func commandRead(ctx Context, parsed ParsedArgs) int {
	reference := ""
	if len(parsed.Positional) > 0 {
		reference = parsed.Positional[0]
	}

	if reference == "" {
		return fail(ctx, errors.New("read needs a channel name or id"))
	}

	limit, err := numberFlag(parsed, "limit", 20)
	if err != nil {
		return fail(ctx, err)
	}

	workspace, _ := parsed.String("workspace")

	store := newStore(ctx)

	account, err := store.Resolve(workspace)
	if err != nil {
		return fail(ctx, err)
	}

	client := newClient(account)
	directory := loadDirectory(ctx, client, account.TeamID)

	channel, err := client.ResolveChannel(reference, directory.Person)
	if err != nil {
		return fail(ctx, err)
	}

	oldest, _ := parsed.String("oldest")
	latest, _ := parsed.String("latest")

	page, err := client.History(slack.HistoryParams{Channel: channel.ID, Limit: limit, Oldest: oldest, Latest: latest})
	if err != nil {
		return fail(ctx, err)
	}

	chronological := make([]slack.Message, 0, len(page.Messages))

	for _, message := range slices.Backward(page.Messages) {
		chronological = append(chronological, message)
	}

	dir := directory.Get()

	names := client.NamesFor(chronological, func(id string) (string, bool) {
		if label := dir.User(id); label != "" {
			return label, true
		}

		return "", false
	})

	emit(ctx.Stdout, "%s · %s (%s) — %d messages, oldest first\n",
		account.TeamName, slack.ChannelLabel(channel, dir), channel.ID, len(chronological))
	emit(ctx.Stdout, "%s\n", slack.RenderMessages(chronological, slack.RenderOptions{Names: names, Scope: dir.Scope()}))

	if page.HasMore && len(chronological) > 0 {
		emit(ctx.Stderr, "more history exists; use --oldest=%s\n", chronological[0].TS)
	}

	return 0
}

func commandThread(ctx Context, parsed ParsedArgs) int {
	reference, threadTS := "", ""

	if len(parsed.Positional) > 0 {
		reference = parsed.Positional[0]
	}

	if len(parsed.Positional) > 1 {
		threadTS = parsed.Positional[1]
	}

	if reference == "" || threadTS == "" {
		return fail(ctx, errors.New("thread needs a channel and a thread_ts"))
	}

	workspace, _ := parsed.String("workspace")

	store := newStore(ctx)

	account, err := store.Resolve(workspace)
	if err != nil {
		return fail(ctx, err)
	}

	client := newClient(account)
	directory := loadDirectory(ctx, client, account.TeamID)

	channel, err := client.ResolveChannel(reference, directory.Person)
	if err != nil {
		return fail(ctx, err)
	}

	messages, err := client.Replies(channel.ID, threadTS)
	if err != nil {
		return fail(ctx, err)
	}

	dir := directory.Get()

	names := client.NamesFor(messages, func(id string) (string, bool) {
		if label := dir.User(id); label != "" {
			return label, true
		}

		return "", false
	})

	emit(ctx.Stdout, "%s · thread in %s (%s) — %d messages\n",
		account.TeamName, slack.ChannelLabel(channel, dir), channel.ID, len(messages))
	emit(ctx.Stdout, "%s\n", slack.RenderMessages(messages, slack.RenderOptions{Names: names, Scope: dir.Scope()}))

	return 0
}
