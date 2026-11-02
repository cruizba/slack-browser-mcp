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

func TestAForumMoveThatIsListedButNotPorted(t *testing.T) {
	t.Parallel()

	m := newMachine(t)

	// `say` and `forget` are in the help table because the surface advertises them;
	// until they are wired, the command says so instead of pretending.
	got := m.run("agent-forum", "say", "builder@laptop", "hello")

	if got.code != 1 || !strings.Contains(got.stderr, "not ported yet") {
		t.Fatalf("got %+v", got)
	}
}
