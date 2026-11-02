package cli_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/cli"
	"github.com/cruizba/slack-browser-mcp/internal/config"
	"github.com/cruizba/slack-browser-mcp/internal/fakeapi"
	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

// machine is one installed copy: a home with a stored session, pointed at a fake Slack
// that serves the fixtures recorded from the real API.
type machine struct {
	t     *testing.T
	env   platform.Env
	paths config.Paths
}

const account = `{
  "team_id": "T000000001",
  "team_name": "CodeURJC",
  "team_domain": "codeurjc",
  "user": "carlos.ruiz",
  "user_id": "U000000001",
  "token": "xoxc-difftest-token",
  "d_cookie": "xoxd-difftest-cookie",
  "d_cookie_name": "d",
  "saved_at": "2026-11-02T00:00:00.000Z"
}
`

func newMachine(t *testing.T) *machine {
	t.Helper()

	server, err := fakeapi.New(filepath.Join("..", "..", "testdata", "live"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("loading fixtures: %v", err)
	}

	// The fixtures are reads; the writes a test needs answer here.
	fake := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/chat.postMessage") {
			response.Header().Set("Content-Type", "application/json")

			_, _ = response.Write([]byte(`{"ok":true,"ts":"1793200000.000100","channel":"C000000002","message":{"text":"hi"}}`))

			return
		}

		server.Handler().ServeHTTP(response, request)
	}))
	t.Cleanup(fake.Close)

	base := fake.URL + "/api/"

	env := platform.Env{
		Getenv: func(name string) string {
			if name == "SLACK_BROWSER_API_BASE" || name == "XDG_TEST_BASE" {
				return base
			}

			return ""
		},
		LookupEnv: func(name string) (string, bool) {
			switch name {
			case "SLACK_BROWSER_API_BASE":
				return base, true
			case "SLACK_BROWSER_PACE_MS":
				return "0", true
			}

			return "", false
		},
		Home: t.TempDir(),
		OS:   "linux",
	}

	paths := config.Resolve(env)

	if err := os.MkdirAll(paths.Accounts, 0o700); err != nil {
		t.Fatalf("creating the account directory: %v", err)
	}

	file := filepath.Join(paths.Accounts, "T000000001.json")

	if err := os.WriteFile(file, []byte(account), 0o600); err != nil {
		t.Fatalf("seeding the account: %v", err)
	}

	if err := os.WriteFile(paths.SettingsFile, []byte(`{"default_workspace":"T000000001"}`), 0o600); err != nil {
		t.Fatalf("seeding the settings: %v", err)
	}

	return &machine{t: t, env: env, paths: paths}
}

func (m *machine) run(args ...string) session {
	m.t.Helper()

	var stdout, stderr bytes.Buffer

	code := cli.Run(args, cli.Context{Env: m.env, Paths: m.paths, Stdout: &stdout, Stderr: &stderr})

	return session{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func TestSendIsADryRunWithoutTheFlag(t *testing.T) {
	t.Parallel()

	m := newMachine(t)

	got := m.run("send", "general", "hello")

	if got.code != 0 {
		t.Fatalf("exit = %d, stderr = %q", got.code, got.stderr)
	}

	if !strings.HasPrefix(got.stdout, "dry run — would post to CodeURJC · #general") {
		t.Fatalf("stdout = %q", got.stdout)
	}

	// Nothing was posted, so there is no audit line to read.
	if _, err := os.Stat(m.paths.AuditLog); err == nil {
		t.Fatal("a dry run wrote the audit log")
	}

	if !strings.Contains(got.stderr, "nothing sent; add --send to commit") {
		t.Fatalf("stderr = %q", got.stderr)
	}
}

func TestSendPostsAndAudits(t *testing.T) {
	t.Parallel()

	m := newMachine(t)

	got := m.run("send", "general", "hello", "--send")

	if got.code != 0 {
		t.Fatalf("exit = %d, stderr = %q", got.code, got.stderr)
	}

	if !strings.HasPrefix(got.stdout, "sent ts 1793200000.000100 to CodeURJC · #general") {
		t.Fatalf("stdout = %q", got.stdout)
	}

	raw, err := os.ReadFile(m.paths.AuditLog)
	if err != nil {
		t.Fatalf("reading the audit log: %v", err)
	}

	var line map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &line); err != nil {
		t.Fatalf("the audit line is not JSON: %v", err)
	}

	if line["workspace"] != "CodeURJC" || line["channel"] != "#general" {
		t.Fatalf("audit = %v", line)
	}

	// The audit holds a preview, never the whole message.
	if _, whole := line["text"]; whole {
		t.Fatalf("the audit kept the whole text: %v", line)
	}
}

func TestSendNeedsAChannelAndAMessage(t *testing.T) {
	t.Parallel()

	m := newMachine(t)

	if got := m.run("send", "general"); got.code != 1 || !strings.Contains(got.stderr, "send needs a channel and a message") {
		t.Fatalf("got %+v", got)
	}
}

func TestAssistSetsShowsAndClears(t *testing.T) {
	t.Parallel()

	m := newMachine(t)

	if got := m.run("assist", "RoboCarlos"); !strings.Contains(got.stdout, "assisted nickname: RoboCarlos") {
		t.Fatalf("set = %q", got.stdout)
	}

	if got := m.run("assist"); !strings.Contains(got.stdout, "assisted nickname: RoboCarlos") {
		t.Fatalf("show = %q", got.stdout)
	}

	if got := m.run("assist", "--clear"); !strings.Contains(got.stdout, "cleared") {
		t.Fatalf("clear = %q", got.stdout)
	}

	// Clearing goes back to the generated default, which names the account.
	got := m.run("assist")
	if !strings.Contains(got.stdout, "no nickname configured") {
		t.Fatalf("after clear = %q", got.stdout)
	}
}

func TestSyncThenArchiveStats(t *testing.T) {
	t.Parallel()

	m := newMachine(t)

	synced := m.run("sync", "--quiet")

	if synced.code != 0 {
		t.Fatalf("sync exited %d: %s", synced.code, synced.stderr)
	}

	if !strings.Contains(synced.stdout, "synced ") {
		t.Fatalf("sync said %q", synced.stdout)
	}

	stats := m.run("archive", "stats")

	if stats.code != 0 {
		t.Fatalf("archive stats exited %d: %s", stats.code, stats.stderr)
	}

	if !strings.Contains(stats.stdout, "messages (") || !strings.Contains(stats.stdout, "in ") {
		t.Fatalf("archive stats = %q", stats.stdout)
	}

	// The index is derived data, so re-indexing answers the same number.
	reindex := m.run("archive", "reindex")
	if !strings.Contains(reindex.stdout, "reindexed ") {
		t.Fatalf("reindex = %q", reindex.stdout)
	}
}

func TestSyncWithNoSessionIsNotAnError(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	env := platform.Env{
		Getenv:    func(string) string { return "" },
		LookupEnv: func(string) (string, bool) { return "", false },
		Home:      home,
		OS:        "linux",
	}

	var stdout, stderr bytes.Buffer

	code := cli.Run([]string{"sync"}, cli.Context{Env: env, Paths: config.Resolve(env), Stdout: &stdout, Stderr: &stderr})

	// The boot sync runs before anybody signs in, so nothing to do is not a failure.
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}

	if !strings.Contains(stderr.String(), "No Slack session stored yet") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestForumListOnAMachineWithNothingBound(t *testing.T) {
	t.Parallel()

	m := newMachine(t)

	got := m.run("agent-forum", "list")

	if got.code != 0 {
		t.Fatalf("exit = %d, stderr = %q", got.code, got.stderr)
	}

	if !strings.Contains(got.stdout, "agent forum as unregistered") {
		t.Fatalf("stdout = %q", got.stdout)
	}

	if !strings.Contains(got.stdout, "no channel bound") {
		t.Fatalf("stdout = %q", got.stdout)
	}

	if !strings.Contains(got.stdout, "timings: ttl ") {
		t.Fatalf("stdout does not print the clocks: %q", got.stdout)
	}
}

func TestForumRegisterAndPostADryRun(t *testing.T) {
	t.Parallel()

	m := newMachine(t)

	registered := m.run("agent-forum", "register", "builder", "--accepts", "review,triage")

	if registered.code != 0 {
		t.Fatalf("register exited %d: %s", registered.code, registered.stderr)
	}

	if !strings.Contains(registered.stdout, "registered builder@") {
		t.Fatalf("register = %q", registered.stdout)
	}

	// Binding needs a channel the session can read, which the fixtures provide.
	bound := m.run("agent-forum", "add", "general")

	if bound.code != 0 {
		t.Fatalf("add exited %d: %s", bound.code, bound.stderr)
	}

	if !strings.Contains(bound.stdout, "bound #general") {
		t.Fatalf("add = %q", bound.stdout)
	}

	// With nobody else in the roster, a post names the missing peer instead of guessing.
	post := m.run("agent-forum", "post", "reviewer@elsewhere", "Review it", "--body", "the diff")

	if post.code == 0 {
		t.Fatalf("a post to an unknown peer exited 0: %q", post.stdout)
	}

	if !strings.Contains(post.stderr, "No peer") {
		t.Fatalf("stderr = %q", post.stderr)
	}
}

func TestUnknownForumMoveListsTheMoves(t *testing.T) {
	t.Parallel()

	m := newMachine(t)

	got := m.run("agent-forum", "levitate")

	if got.code != 1 {
		t.Fatalf("exit = %d", got.code)
	}

	// The whitelist refuses a subcommand it does not know before anything runs, and
	// says which ones exist.
	if !strings.Contains(got.stderr, "unknown subcommand \"levitate\"") ||
		!strings.Contains(got.stderr, "try list, add, remove") {
		t.Fatalf("stderr = %q", got.stderr)
	}
}

func TestForumSayWithoutAnIdentity(t *testing.T) {
	t.Parallel()

	m := newMachine(t)

	// Nothing says who is speaking, and a local line carries that name, so the answer
	// is the one command that fixes it.
	got := m.run("agent-forum", "say", "builder@laptop", "hello")

	if got.code != 1 || !strings.Contains(got.stderr, "register <name>") {
		t.Fatalf("got %+v", got)
	}
}

func TestForumSayWithNoDaemon(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	m.run("agent-forum", "register", "ana")

	got := m.run("agent-forum", "say", "builder@laptop", "stop, the branch moved")

	// Nothing records a local line, so a nudge with nobody to hear it is refused
	// instead of being dropped: the answer is to post to Slack.
	if got.code != 1 || !strings.Contains(got.stderr, "no forum daemon is running") {
		t.Fatalf("got %+v", got)
	}

	if !strings.Contains(got.stderr, "post to Slack instead") {
		t.Fatalf("the refusal does not say what to do: %q", got.stderr)
	}
}

func TestForumSayNeedsSomethingToSay(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	m.run("agent-forum", "register", "ana")

	got := m.run("agent-forum", "say", "builder@laptop")

	if got.code != 1 || !strings.Contains(got.stderr, "needs <name@host> and something to say") {
		t.Fatalf("got %+v", got)
	}
}

func TestForumForgetRetiresALabelHere(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	m.run("agent-forum", "register", "ana")

	got := m.run("agent-forum", "forget", "alice@old")

	if got.code != 0 {
		t.Fatalf("forget failed: %+v", got)
	}

	// The point of the command is that Slack is untouched: only this machine changed.
	if !strings.Contains(got.stdout, "retired alice@old here") ||
		!strings.Contains(got.stdout, "nothing was posted to Slack") {
		t.Fatalf("stdout = %q", got.stdout)
	}

	file, err := os.ReadFile(m.paths.ForumFile)
	if err != nil {
		t.Fatalf("reading the forum file: %v", err)
	}

	var stored struct {
		Ignored []string `json:"ignored"`
	}

	if err := json.Unmarshal(file, &stored); err != nil {
		t.Fatalf("the forum file is not JSON: %v", err)
	}

	if len(stored.Ignored) != 1 || stored.Ignored[0] != "alice@old" {
		t.Fatalf("ignored = %v", stored.Ignored)
	}

	again := m.run("agent-forum", "forget", "alice@old")

	if again.code != 0 || !strings.Contains(again.stdout, "already retired") {
		t.Fatalf("forgetting twice = %+v", again)
	}
}

func TestForumForgetKeepsALabelNobodyRegistered(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	m.run("agent-forum", "register", "ana")

	got := m.run("agent-forum", "forget", "@alice@old")

	// No roster entry to resolve against, so the label is kept as typed — the entry
	// worth retiring is usually the one whose register already aged out — and the
	// leading @ a person types is dropped.
	if got.code != 0 || !strings.Contains(got.stdout, "retired alice@old here") {
		t.Fatalf("got %+v", got)
	}
}

func TestForumForgetRefusesANameThatIsTwoMachines(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	m.run("agent-forum", "register", "ana")

	got := m.run("agent-forum", "forget", "@")

	if got.code != 1 || !strings.Contains(got.stderr, "needs a name or a name@host") {
		t.Fatalf("got %+v", got)
	}
}

func TestThreadReadsSlackAndKeepsIt(t *testing.T) {
	t.Parallel()

	m := newMachine(t)

	// The ts comes from the recorded fixtures: a thread the fake knows, asked for by
	// name the way a person would.
	got := m.run("thread", "general", "1792500000.000100")

	if got.code != 0 {
		t.Fatalf("thread failed: %+v", got)
	}

	if !strings.Contains(got.stdout, "thread") {
		t.Fatalf("stdout = %q", got.stdout)
	}

	missing := m.run("thread", "general")
	if missing.code != 1 || !strings.Contains(missing.stderr, "needs a channel and a thread_ts") {
		t.Fatalf("a thread with no ts = %+v", missing)
	}
}

func TestArchiveSearchNeedsAQuery(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	m.run("sync", "--quiet")

	got := m.run("archive", "search")

	if got.code != 1 || !strings.Contains(got.stderr, "needs a query") {
		t.Fatalf("got %+v", got)
	}
}

func TestArchiveSearchSaysWhenNothingMatches(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	m.run("sync", "--quiet")

	got := m.run("archive", "search", "zzz-nothing-matches-this")

	if got.code != 0 {
		t.Fatalf("search failed: %+v", got)
	}

	// An empty result is an answer, not an error: the archive is local and complete as
	// far as it goes, so a miss is information.
	if !strings.Contains(got.stdout, "no hits") {
		t.Fatalf("stdout = %q, want it to say there were no hits", got.stdout)
	}
}

func TestArchiveSearchFindsWhatTheSyncFiled(t *testing.T) {
	t.Parallel()

	m := newMachine(t)
	m.run("sync", "--quiet")

	// Every archived message contains this word in the fixtures; the search has to come
	// back with at least one line, which is what makes the index worth building.
	got := m.run("archive", "search", "--limit", "3", "the")

	if got.code != 0 {
		t.Fatalf("search failed: %+v", got)
	}

	if strings.TrimSpace(got.stdout) == "" {
		t.Fatal("the search found nothing in an archive the sync just filled")
	}
}

func TestSendDryRunWithAttachments(t *testing.T) {
	t.Parallel()

	m := newMachine(t)

	report := filepath.Join(t.TempDir(), "report.md")
	if err := os.WriteFile(report, []byte("# a report"), 0o600); err != nil {
		t.Fatalf("writing the attachment: %v", err)
	}

	got := m.run("send", "general", "see the report", "--attach", report)

	if got.code != 0 {
		t.Fatalf("dry run failed: %+v", got)
	}

	// The preview names the file and its type without sending a byte of it, which is
	// the whole point of a dry run that takes attachments.
	if !strings.Contains(got.stdout, "report.md") || !strings.Contains(got.stdout, "text/markdown") {
		t.Fatalf("stdout = %q", got.stdout)
	}

	missing := m.run("send", "general", "see the report", "--attach", filepath.Join(t.TempDir(), "gone.md"))
	if missing.code != 1 || !strings.Contains(missing.stderr, "No file at") {
		t.Fatalf("a missing attachment = %+v", missing)
	}
}
