package cli

import (
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/cruizba/slack-browser-mcp/internal/config"
)

// What `slack-browser help` prints, what `<command> --help` prints, and which
// flags a command may read. The pages and the whitelist are one table because a
// page that drifts from the flags the code reads is worse than no page, and a
// flag the table does not know is refused before the command runs.
const (
	program    = "slack-browser"
	pageWidth  = 80
	undocument = "(undocumented)"
)

type flagDoc struct {
	value string
	doc   string
}

var flagDocs = map[string]flagDoc{
	"accepts":        {value: "<tag,...>", doc: "the kinds of work this agent takes, announced with its name and shown in peers"},
	"all":            {doc: "also list the tasks already decided on this machine"},
	"no-assist":      {doc: "post the text alone, without the AI-agent header"},
	"attach":         {value: "<path,...>", doc: "local files to upload with the message, comma-separated"},
	"backfill":       {doc: "read the whole channel history, not just what is new"},
	"binding":        {value: "<name>", doc: "which bound channel to post in, when several are"},
	"body":           {value: "<text>", doc: "the message body of the task or reply"},
	"channel":        {value: "<name|id>", doc: "restrict the search to one channel"},
	"capability":     {value: "<tag>", doc: "the kind of work a task needs; on a broadcast, anyone who accepts it may take it"},
	"check":          {doc: "say what would happen and change nothing"},
	"clear":          {doc: "drop the setting instead of reading it"},
	"deadline":       {value: "<seconds|iso>", doc: "seconds the run may take (sync, wait, poll) or the ISO time a task expires (forum post)"},
	"deleted":        {doc: "include the messages Slack no longer serves"},
	"fail":           {doc: "report the task as failed instead of done"},
	"follow":         {doc: "keep waiting for the next task instead of printing what is there"},
	"full":           {doc: "re-fetch the whole backfill window instead of the delta"},
	"headless":       {doc: "sign in without a visible browser window"},
	"host":           {value: "<name>", doc: "the host half of this machine identity (default: its hostname)"},
	"json":           {doc: "print one JSON object per line instead of prose"},
	"latest":         {value: "<ts>", doc: "Slack ts to bound the page on the newer side"},
	"limit":          {value: "<n>", doc: "how many messages or hits to show (default 20)"},
	"no-post":        {doc: "keep the change local instead of announcing it"},
	"no-presence":    {doc: "never announce this machine in that channel"},
	"offline":        {doc: "read the archive only; do not refresh from Slack"},
	"oldest":         {value: "<ts>", doc: "Slack ts to bound the page on the older side"},
	"parent":         {value: "<id>", doc: "the task this one continues"},
	"post":           {doc: "post for real; without it, print the message and change nothing"},
	"force":          {doc: "go ahead even when the thread has reply(ies) that would lose their parent"},
	"pre":            {doc: "consider prerelease tags when picking the latest"},
	"quiet":          {doc: "one line per workspace instead of per channel"},
	"reconcile-days": {value: "<days>", doc: "how far back to re-check edits and deletions (default 7)"},
	"send":           {doc: "post for real; without it, print the message and change nothing"},
	"signed":         {doc: "sign every post in that channel"},
	"since":          {value: "<days>", doc: "days of history the first backfill covers (default 90)"},
	"team":           {value: "<ref>", doc: "same as --workspace"},
	"thread":         {value: "<ts>", doc: "post inside that thread instead of the channel"},
	"timeout":        {value: "<seconds>", doc: "give up after that long"},
	"to":             {value: "<tag>", doc: "the release to move to (default: latest stable)"},
	"workspace":      {value: "<ref>", doc: "pick the stored session by name, domain or team id"},
}

// subcommand is one page under a command that has several.
type subcommand struct {
	usage string
	flags []string
	notes []string
}

// command carries its subcommands both ways round: in the order the page lists
// them, and keyed for lookup. A map alone would print them shuffled.
type command struct {
	summary     string
	flags       []string
	notes       []string
	order       []string
	subcommands map[string]subcommand
}

var commandOrder = []string{
	"login", "accounts", "workspace", "assist", "status", "read", "thread",
	"send", "sync", "archive", "agent-forum", "update", "version", "serve",
}

var commandTable = map[string]command{
	"login": {
		summary: program + " login [--timeout <seconds>] [--headless]",
		flags:   []string{"timeout", "headless"},
		notes: []string{
			"Opens Chrome once and keeps the session it finds there; nothing else in the tool opens a browser. The `xoxc-` token and the `xoxd-` cookie are stored at ~/.config/slack-browser-mcp/accounts with mode 0600.",
			"Sign in to one workspace at a time. The first one becomes the default, and a later run adds a workspace instead of replacing the first.",
		},
	},
	"accounts": {
		summary: program + " accounts",
		flags:   []string{},
		notes: []string{
			"One line per stored workspace. `*` marks the default, and the last line says what that choice came from.",
		},
	},
	"workspace": {
		summary: program + " workspace <list|use|clear>",
		notes:   []string{"The default workspace applies to every call that omits --workspace."},
		order:   []string{"list", "use", "clear"},
		subcommands: map[string]subcommand{
			"list": {
				usage: program + " workspace [list]",
				flags: []string{},
				notes: []string{"Same output as `accounts`."},
			},
			"use": {
				usage: program + " workspace use <name|domain|team-id>",
				flags: []string{},
				notes: []string{"Saves the default, which outlives the session and the agent that set it."},
			},
			"clear": {
				usage: program + " workspace clear",
				flags: []string{},
				notes: []string{
					"The saved default goes away. With one stored session that session is still used implicitly; the rule only bites once a second one exists.",
				},
			},
		},
	},
	"assist": {
		summary: program + " assist [<nickname>] [--clear]",
		flags:   []string{"clear"},
		notes: []string{
			"With no argument it prints what is in force. A nickname replaces the generated default in the header that opens every sent message.",
			"--no-assist on `send` skips the header for one message, and " + config.AssistedNicknameEnv + " wins over the saved nickname.",
		},
	},
	"status": {
		summary: program + " status [--workspace <ref>]",
		flags:   []string{"workspace"},
		notes: []string{
			"Asks Slack whether each stored session still works, one workspace at a time. Exit code 2 when any of them fails.",
		},
	},
	"read": {
		summary: program + " read <channel> [--limit <n>] [--oldest <ts>] [--latest <ts>] [--workspace <ref>]",
		flags:   []string{"limit", "oldest", "latest", "workspace"},
		notes: []string{
			"Oldest first. Slack serves 100 messages at most, and a page that leaves more behind says so on stderr with the `--oldest` to continue from.",
		},
	},
	"thread": {
		summary: program + " thread <channel> <thread_ts> [--workspace <ref>]",
		flags:   []string{"workspace"},
		notes:   []string{"Every message in the thread, oldest first."},
	},
	"send": {
		summary: program + " send <channel> <message> [--thread <ts>] [--attach <path,...>] [--send] [--no-assist] [--workspace <ref>]",
		flags:   []string{"thread", "attach", "send", "no-assist", "workspace"},
		notes: []string{
			"A dry run unless --send: it prints the final text, header included, and posts nothing. Every real send is written to the audit log.",
			"Attachments go up as files in the same message as the text, so the thread and the comment arrive together.",
		},
	},
	"sync": {
		summary: program + " sync [--full] [--since <days>] [--deadline <seconds>] [--quiet] [--workspace <ref>]",
		flags:   []string{"full", "since", "deadline", "quiet", "reconcile-days", "workspace"},
		notes: []string{
			"Refreshes the local archive from every stored workspace unless --workspace names one. The deadline is one budget for the whole run, not per workspace.",
			"It upgrades an archive written by an older layout before touching it. The server starts one at session start with the same deadline.",
		},
	},
	"archive": {
		summary: program + " archive <search|stats|reindex|migrate>",
		notes:   []string{"Everything here reads the archive that `sync` writes, so it works offline."},
		order:   []string{"search", "stats", "reindex", "migrate"},
		subcommands: map[string]subcommand{
			"search": {
				usage: program + " archive search <query> [--channel <name>] [--workspace <ref>] [--limit <n>] [--deleted]",
				flags: []string{"channel", "workspace", "limit", "deleted"},
				notes: []string{
					"Full-text and ranked, across every workspace. It reaches further back than a channel read, which is the point of keeping an archive.",
				},
			},
			"stats": {
				usage: program + " archive stats [--workspace <ref>]",
				flags: []string{"workspace", "team"},
				notes: []string{"How many messages and channels are filed, and how far back they go."},
			},
			"reindex": {
				usage: program + " archive reindex",
				flags: []string{},
				notes: []string{"Rebuilds the search index from the JSONL. The archive itself is not touched."},
			},
			"migrate": {
				usage: program + " archive migrate",
				flags: []string{},
				notes: []string{"Upgrades an archive written by an older layout. `sync` does the same before it starts."},
			},
		},
	},
	"agent-forum": {
		summary: program + " agent-forum <subcommand> [args]",
		notes: []string{
			"The channel where agents hand each other work: bind one, register a name, then address a peer by name. Every write is a dry run unless --post, and every post is audited.",
			"`forum` is an alias of `agent-forum`.",
		},
		order: []string{
			"list", "add", "remove", "register", "unregister", "peers", "away", "inbox",
			"task", "post", "accept", "decline", "progress", "result", "ack", "reopen",
			"cancel", "delete", "edit", "wait", "poll", "daemon", "say", "forget",
		},
		subcommands: map[string]subcommand{
			"list": {
				usage: program + " agent-forum [list]",
				flags: []string{},
				notes: []string{"Bound channels, this machine identity, the timings in force and any retired peers."},
			},
			"add": {
				usage: program + " agent-forum add <channel> [--backfill] [--signed] [--no-presence] [--workspace <ref>]",
				flags: []string{"backfill", "signed", "no-presence", "workspace"},
				notes: []string{"Binds a channel from the current ts forward; --backfill reads what came before."},
			},
			"remove": {
				usage: program + " agent-forum remove <channel>",
				flags: []string{},
				notes: []string{"Unbinds the channel. The decision log for its tasks is kept."},
			},
			"register": {
				usage: program + " agent-forum register <name> [--host <name>] [--accepts <tag,...>] [--no-post]",
				flags: []string{"host", "accepts", "no-post"},
				notes: []string{
					"The announcement goes out on its own: a peer nobody can discover is a peer nobody can address. --no-post keeps the name local.",
					"--accepts says which kinds of work this agent takes; it travels in the same announcement, and registering again with a different list re-announces it.",
				},
			},
			"unregister": {
				usage: program + " agent-forum unregister [<name>] [--no-post]",
				flags: []string{"no-post"},
				notes: []string{"--no-post keeps the departure quiet; peers then hold the name until the claim expires."},
			},
			"peers": {
				usage: program + " agent-forum peers [--workspace <ref>]",
				flags: []string{"workspace"},
				notes: []string{"Who else is in the bound channels, and whether they are around."},
			},
			"away": {
				usage: program + " agent-forum away [<note>]",
				flags: []string{},
				notes: []string{"Tells the bound channels this machine is stepping out, with an optional note."},
			},
			"inbox": {
				usage: program + " agent-forum inbox [--all] [--json] [--offline]",
				flags: []string{"all", "json", "offline"},
				notes: []string{"What is waiting for this machine, refreshing from Slack first unless --offline."},
			},
			"task": {
				usage: program + " agent-forum task <id>",
				flags: []string{"offline"},
				notes: []string{"The full thread and state of one task, by id or a unique prefix of it."},
			},
			"post": {
				usage: program + " agent-forum post <to> <title> --body <text> [--capability <tag>] [--parent <id>] [--deadline <iso>] [--binding <name>] [--post]",
				flags: []string{"body", "capability", "parent", "deadline", "binding", "post"},
				notes: []string{"Hands work to a peer by name. --capability says what kind of work it is."},
			},
			"accept": {
				usage: program + " agent-forum accept <id> [--body <text>] [--post]",
				flags: []string{"body", "post"},
				notes: []string{"Claims a task addressed to this machine, so the other side stops waiting."},
			},
			"decline": {
				usage: program + " agent-forum decline <id> [--body <text>] [--post]",
				flags: []string{"body", "post"},
				notes: []string{"Says no before claiming it; the body is the reason."},
			},
			"progress": {
				usage: program + " agent-forum progress <id> --body <text> [--post]",
				flags: []string{"body", "post"},
				notes: []string{"A note inside the thread while the work is going on. One per real milestone, not a heartbeat."},
			},
			"result": {
				usage: program + " agent-forum result <id> --body <text> [--fail] [--post]",
				flags: []string{"body", "fail", "post"},
				notes: []string{"Reports the outcome. Without --fail it reads as done; with it, as failed."},
			},
			"ack": {
				usage: program + " agent-forum ack <id> [--body <text>] [--post]",
				flags: []string{"body", "post"},
				notes: []string{"The requester closes a reported task."},
			},
			"reopen": {
				usage: program + " agent-forum reopen <id> --body <text> [--post]",
				flags: []string{"body", "post"},
				notes: []string{"The requester sends a reported task back, with what is missing."},
			},
			"cancel": {
				usage: program + " agent-forum cancel <id> [--body <text>] [--post]",
				flags: []string{"body", "post"},
				notes: []string{"Withdraws a task that is still open or accepted. Only the requester can."},
			},
			"delete": {
				usage: program + " agent-forum delete <id|ts> [--force] [--post]",
				flags: []string{"force", "post"},
				notes: []string{
					"Deletes a message this account posted, and the task it carried reads `deleted` afterwards. A thread root with replies needs `--force`, because those replies would be left answering nothing.",
					"A preview first: without `--post` it prints what would go and changes nothing. Every deletion leaves an audit line with a preview of the text, because after it there is nothing left to read.",
				},
			},
			"edit": {
				usage: program + " agent-forum edit <id> --body <text> [--post]",
				flags: []string{"body", "post"},
				notes: []string{
					"Rewrites the body of a task you posted. Slack cannot swap the file on a message, so the body is re-posted as a new envelope with the same id and the previous message is deleted; the ledger shows it as edited.",
					"Refused once the thread has replies: replacing the root would leave them hanging off a message that no longer exists. Use `reopen` or a `progress` note for that.",
				},
			},
			"wait": {
				usage: program + " agent-forum wait [--timeout <s>] [--follow] [--json] [--offline]",
				flags: []string{"timeout", "deadline", "follow", "json", "offline"},
				notes: []string{"Blocks until work lands, or prints what is already there."},
			},
			"poll": {
				usage: program + " agent-forum poll [--quiet] [--deadline <s>]",
				flags: []string{"quiet", "deadline"},
				notes: []string{"One refresh pass, for checking by hand what the daemon does on its own."},
			},
			"daemon": {
				usage: program + " agent-forum daemon [start|stop|status|serve]",
				flags: []string{},
				notes: []string{
					"The daemon owns the archive and serves every local agent over a socket, so one refresh covers the whole machine. It starts when a client needs it and leaves after a window with nobody connected.",
					"`serve` is the foreground process a client starts for itself; it is not meant to be typed. `status` says whether it is up and who is attached.",
				},
			},
			"say": {
				usage: program + " agent-forum say <name@host> <text>",
				flags: []string{},
				notes: []string{
					"Delivers a line straight to an agent connected to this machine, without posting to Slack. The peer has to be connected: nothing records a local message, so it is a nudge and not the work itself.",
				},
			},
			"forget": {
				usage: program + " agent-forum forget <name|name@host>",
				flags: []string{},
				notes: []string{"Retires a peer locally, so its tasks stop showing up in the inbox."},
			},
		},
	},
	"update": {
		summary: program + " update [--check] [--to <tag>] [--pre]",
		flags:   []string{"check", "to", "pre"},
		notes: []string{
			"Moves the checkout this build runs from to a release: the latest semver tag, or the one named with --to. It fetches the tags, checks the tag out detached and re-runs `install.sh --no-pull` to rebuild and relink.",
			"--check only says what it would do. A checkout on main that is already past the latest release is left alone. Restart the agent afterwards so its MCP server loads the new build.",
		},
	},
	"version": {
		summary: program + " version",
		flags:   []string{},
		notes:   []string{"The package version and the tag or branch the checkout stands on."},
	},
	"serve": {
		summary: program + " serve",
		flags:   []string{},
		notes:   []string{"The MCP server on stdio. `install.sh` registers it with each agent; it is not meant to be typed."},
	},
}

var aliases = map[string]string{
	"forum":      "agent-forum",
	"workspaces": "workspace",
	"-v":         "version",
	"--version":  "version",
}

// What such a command runs with when no subcommand is named.
var defaultSubcommands = map[string]string{
	"agent-forum": "list",
	"archive":     "stats",
	"workspace":   "list",
}

var indexNotes = []string{
	"The browser opens once, during login. Everything else talks to the Slack Web API with the captured session, stored at ~/.config/slack-browser-mcp/accounts with mode 0600.",
	"Without --workspace, a command uses SLACK_BROWSER_MCP_TEAM, then the default saved by \"workspace use\", then the only stored session. \"sync\" without --workspace covers every stored workspace.",
	"With nothing configured, every sent message is still marked: the header reads \"*Message Sent by AI Agent '<client>' of <account>*\", naming the MCP client and the account it goes out from. Set your own with \"assist <nickname>\"; --no-assist posts the text alone, and " + config.AssistedNicknameEnv + " wins over config.json.",
	"\"agent-forum\" (alias \"forum\") is the channel where agents hand each other work: bind a channel once, register a name, then post tasks to a peer by name. Every write is a dry run unless --post, and every post is audited.",
	"The forum runs behind a local daemon: it starts when an agent needs it, serves every agent on the machine over a socket, and exits after a quiet window. There is no service to enable for it.",
	"Every command answers --help with its own page: what it does, its usage and the flags it takes. `slack-browser help <command>` prints the same page.",
}

// wrap breaks a note at the page width, greedily, which is what the printed
// pages have always looked like.
func wrap(text string) string {
	var b strings.Builder

	line := ""

	for word := range strings.FieldsSeq(text) {
		switch {
		case line == "":
			line = word
		case utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) <= pageWidth:
			line += " " + word
		default:
			b.WriteString(line + "\n")
			line = word
		}
	}

	if line != "" {
		b.WriteString(line)
	}

	return b.String()
}

func flagLines(names []string) []string {
	specs := flagSpecNames(names)

	width := 0

	for _, spec := range specs {
		width = max(width, len(spec[0]))
	}

	lines := make([]string, 0, len(specs))

	for _, spec := range specs {
		lines = append(lines, "  "+spec[0]+strings.Repeat(" ", width-len(spec[0]))+"  "+spec[1])
	}

	return lines
}

// flagSpecNames returns the sorted `--name value` and doc pairs a page lists,
// one per flag, however many subcommands mention it.
func flagSpecNames(names []string) [][2]string {
	seen := make(map[string]bool, len(names))
	unique := make([]string, 0, len(names))

	for _, name := range names {
		if !seen[name] {
			seen[name] = true
			unique = append(unique, name)
		}
	}

	sort.Strings(unique)

	specs := make([][2]string, 0, len(unique))

	for _, name := range unique {
		key := "--" + name
		doc := undocument

		if spec, ok := flagDocs[name]; ok {
			if spec.value != "" {
				key += " " + spec.value
			}

			if spec.doc != "" {
				doc = spec.doc
			}
		}

		specs = append(specs, [2]string{key, doc})
	}

	return specs
}

func subcommandOf(name, sub string) (subcommand, bool) {
	spec, ok := commandTable[name]
	if !ok || sub == "" || spec.subcommands == nil {
		return subcommand{}, false
	}

	found, ok := spec.subcommands[sub]

	return found, ok
}

func flagsOf(name, sub string) []string {
	spec, ok := commandTable[name]
	if !ok {
		return nil
	}

	if len(spec.subcommands) == 0 {
		return spec.flags
	}

	if sub != "" {
		if found, ok := subcommandOf(name, sub); ok {
			return found.flags
		}

		return nil
	}

	var all []string

	for _, key := range spec.order {
		all = append(all, spec.subcommands[key].flags...)
	}

	return all
}

// resolveCommand answers the table key for a command or one of its aliases.
func resolveCommand(name string) (string, bool) {
	if key, ok := aliases[name]; ok {
		name = key
	}

	_, ok := commandTable[name]

	return name, ok
}

func commandNames() []string {
	return commandOrder
}

func subcommandNames(name string) []string {
	if spec, ok := commandTable[name]; ok {
		return spec.order
	}

	return nil
}

// flagSpecs is every flag a command accepts, in the shape parseArgs needs.
func flagSpecs(name string) map[string]flagDoc {
	specs := make(map[string]flagDoc)

	for _, flag := range flagsOf(name, "") {
		if spec, ok := flagDocs[flag]; ok {
			specs[flag] = spec

			continue
		}

		specs[flag] = flagDoc{}
	}

	return specs
}

func defaultSubcommand(name string) string {
	return defaultSubcommands[name]
}

// helpRequest is where `--help` or `-h` sits in the arguments after the
// command, or -1.
func helpRequest(tokens []string) int {
	end := len(tokens)

	for i, token := range tokens {
		if token == "--" {
			end = i

			break
		}
	}

	for i := range end {
		if tokens[i] == "--help" || tokens[i] == "-h" {
			return i
		}
	}

	return -1
}

// helpPageFor is the page a help flag asks for, or false when there is no help
// flag or it sits in message text, as in a send whose body mentions --help.
func helpPageFor(commandName string, tokens []string) (string, bool) {
	name, ok := resolveCommand(commandName)
	if !ok {
		return "", false
	}

	at := helpRequest(tokens)
	if at == -1 {
		return "", false
	}

	if at == 0 {
		return commandPage(name, "")
	}

	for _, token := range tokens {
		if strings.HasPrefix(token, "-") {
			continue
		}

		// Only a real subcommand makes the token a topic: `send general --help`
		// carries a channel, and answering it would swallow the message text.
		if _, known := subcommandOf(name, token); known {
			return commandPage(name, token)
		}

		return "", false
	}

	return "", false
}

func commandPage(commandName, sub string) (string, bool) {
	name, ok := resolveCommand(commandName)
	if !ok {
		return "", false
	}

	spec := commandTable[name]

	found, hasSub := subcommandOf(name, sub)
	if sub != "" && spec.subcommands != nil && !hasSub {
		return "", false
	}

	usage := spec.summary
	if sub != "" {
		usage = found.usage
	}

	lines := []string{"Usage: " + usage}
	lines = append(lines, listingOf(spec, sub)...)

	for _, note := range append(append([]string{}, spec.notes...), found.notes...) {
		lines = append(lines, "", wrap(note))
	}

	flags := flagsOf(name, sub)
	if sub != "" {
		flags = found.flags
	}

	if len(flags) > 0 {
		lines = append(lines, "", "Flags:")
		lines = append(lines, flagLines(flags)...)
	}

	if spec.subcommands != nil && sub == "" {
		lines = append(lines, "", "Run `"+program+" "+name+" <subcommand> --help` for one of them.")
	}

	return strings.Join(lines, "\n"), true
}

// listingOf is the block of subcommand usages a command page carries, and
// nothing on the page of one of them.
func listingOf(spec command, sub string) []string {
	if spec.subcommands == nil || sub != "" {
		return nil
	}

	lines := []string{"", "Subcommands:"}

	for _, key := range spec.order {
		lines = append(lines, "  "+spec.subcommands[key].usage)
	}

	return lines
}

func indexPage() string {
	lines := []string{program + " — Slack read access from a browser session", "", "Usage:"}

	for _, name := range commandOrder {
		spec := commandTable[name]

		if spec.subcommands != nil {
			for _, key := range spec.order {
				lines = append(lines, "  "+spec.subcommands[key].usage)
			}

			continue
		}

		lines = append(lines, "  "+spec.summary)
	}

	lines = append(lines, "  "+program+" help [<command>]")

	for _, note := range indexNotes {
		lines = append(lines, "", wrap(note))
	}

	return strings.Join(lines, "\n")
}

// usageProblem is what is wrong with the subcommand and the flags a call came
// with, or the empty string. parseArgs has already refused what no command
// takes; this is the finer whitelist, where a flag belongs to another
// subcommand or to none.
func usageProblem(commandName, sub string, flagNames []string) string {
	name, ok := resolveCommand(commandName)
	if !ok {
		return ""
	}

	spec := commandTable[name]
	hint := "run `" + program + " " + name + " --help` to see what it takes"

	if sub != "" && spec.subcommands != nil {
		if _, known := subcommandOf(name, sub); !known {
			return program + " " + name + ": unknown subcommand \"" + sub + "\"\ntry " + strings.Join(spec.order, ", ")
		}
	}

	allowed := flagsOf(name, sub)

	var unknown []string

	for _, flag := range flagNames {
		if !slices.Contains(allowed, flag) {
			unknown = append(unknown, flag)
		}
	}

	if len(unknown) == 0 {
		return ""
	}

	what := "flag"
	if len(unknown) > 1 {
		what = "flags"
	}

	rendered := make([]string, 0, len(unknown))
	for _, flag := range unknown {
		rendered = append(rendered, "--"+flag)
	}

	return program + " " + name + ": unknown " + what + " " + strings.Join(rendered, ", ") + "\n" + hint
}
