package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/cli"
	"github.com/cruizba/slack-browser-mcp/internal/config"
	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

type session struct {
	code   int
	stdout string
	stderr string
}

// harness is one machine: a home of its own, kept across the calls that make up
// a test, because a command that saves something is only proved by the next one
// reading it back.
type harness struct {
	t     *testing.T
	env   platform.Env
	paths config.Paths
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	env := platform.Env{
		Getenv:    func(string) string { return "" },
		LookupEnv: func(string) (string, bool) { return "", false },
		Home:      t.TempDir(),
		OS:        "linux",
	}

	return &harness{t: t, env: env, paths: config.Resolve(env)}
}

func (h *harness) run(args ...string) session {
	h.t.Helper()

	var stdout, stderr bytes.Buffer

	code := cli.Run(args, cli.Context{Env: h.env, Paths: h.paths, Stdout: &stdout, Stderr: &stderr})

	return session{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func (h *harness) seed(teamID, name, domain string) {
	h.t.Helper()

	if err := os.MkdirAll(h.paths.Accounts, 0o700); err != nil {
		h.t.Fatalf("creating the account directory: %v", err)
	}

	body := `{
  "team_id": "` + teamID + `",
  "team_name": "` + name + `",
  "team_domain": "` + domain + `",
  "user": "ana",
  "user_id": "U01ANA",
  "token": "xoxc-1234567890-abcdef",
  "d_cookie": "xoxd-abcdef%2Bghij",
  "d_cookie_name": "d",
  "saved_at": "2026-10-20T15:00:00.000Z"
}
`

	file := filepath.Join(h.paths.Accounts, teamID+".json")
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		h.t.Fatalf("writing %s: %v", file, err)
	}

	if err := os.Chmod(file, 0o600); err != nil {
		h.t.Fatalf("chmod: %v", err)
	}
}

func TestIndexPageIsTheDefault(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{}, {"help"}, {"--help"}, {"-h"}} {
		got := newHarness(t).run(args...)

		if got.code != 0 {
			t.Fatalf("%v exited %d", args, got.code)
		}

		if !strings.HasPrefix(got.stdout, "slack-browser — Slack read access from a browser session") {
			t.Fatalf("%v printed:\n%s", args, got.stdout)
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	t.Parallel()

	got := newHarness(t).run("nope")

	if got.code != 1 {
		t.Fatalf("exit = %d", got.code)
	}

	if !strings.HasPrefix(got.stderr, "Unknown command: nope\n\n") {
		t.Fatalf("stderr = %q", got.stderr)
	}
}

func TestHelpForAnUnknownTopic(t *testing.T) {
	t.Parallel()

	got := newHarness(t).run("help", "nope")

	if got.code != 1 {
		t.Fatalf("exit = %d", got.code)
	}

	if !strings.HasPrefix(got.stderr, "no command named `nope`\n\n") {
		t.Fatalf("stderr = %q", got.stderr)
	}
}

func TestHelpForACommand(t *testing.T) {
	t.Parallel()

	got := newHarness(t).run("help", "read")

	if got.code != 0 {
		t.Fatalf("exit = %d", got.code)
	}

	if !strings.HasPrefix(got.stdout, "Usage: slack-browser read ") {
		t.Fatalf("stdout = %q", got.stdout)
	}
}

func TestAccountsWithNothingStored(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"accounts"}, {"workspace"}, {"workspaces", "list"}} {
		got := newHarness(t).run(args...)

		if got.code != 0 {
			t.Fatalf("%v exited %d", args, got.code)
		}

		if got.stdout != "No stored session. Run `slack-browser login`.\n" {
			t.Fatalf("%v printed %q", args, got.stdout)
		}
	}
}

func TestAccountsPrintsTheOnlySessionAsTheDefault(t *testing.T) {
	t.Parallel()

	home := newHarness(t)
	home.seed("T01ACME", "Acme", "acme")

	got := home.run("accounts")

	if got.code != 0 {
		t.Fatalf("exit = %d, stderr = %q", got.code, got.stderr)
	}

	want := "* T01ACME  Acme  ana  acme  never validated\n" +
		"default workspace: Acme (T01ACME) via the only stored session\n"

	if got.stdout != want {
		t.Fatalf("stdout:\n%s\nwant:\n%s", got.stdout, want)
	}
}

func TestWorkspaceClear(t *testing.T) {
	t.Parallel()

	got := newHarness(t).run("workspace", "clear")

	if got.code != 0 {
		t.Fatalf("exit = %d, stderr = %q", got.code, got.stderr)
	}

	if got.stdout != "default workspace cleared; the only stored session is still used implicitly\n" {
		t.Fatalf("stdout = %q", got.stdout)
	}
}

func TestWorkspaceUseNeedsASession(t *testing.T) {
	t.Parallel()

	got := newHarness(t).run("workspace", "use", "acme")

	if got.code != 1 {
		t.Fatalf("exit = %d", got.code)
	}

	if !strings.Contains(got.stderr, "No Slack session stored yet") {
		t.Fatalf("stderr = %q", got.stderr)
	}
}

func TestWorkspaceUseSavesTheDefault(t *testing.T) {
	t.Parallel()

	home := newHarness(t)
	home.seed("T01ACME", "Acme", "acme")
	home.seed("T02VIDEO", "Acme Video", "video")

	got := home.run("workspace", "use", "video")

	if got.code != 0 {
		t.Fatalf("exit = %d, stderr = %q", got.code, got.stderr)
	}

	if got.stdout != "default workspace: Acme Video (T02VIDEO)\n" {
		t.Fatalf("stdout = %q", got.stdout)
	}
}

func TestUnknownFlagsAreRefusedBeforeTheCommandRuns(t *testing.T) {
	t.Parallel()

	got := newHarness(t).run("update", "--chek")

	if got.code != 1 {
		t.Fatalf("exit = %d", got.code)
	}

	if !strings.Contains(got.stderr, "unknown flag --chek") {
		t.Fatalf("stderr = %q", got.stderr)
	}
}

func TestFlagsOfAnotherSubcommandAreRefused(t *testing.T) {
	t.Parallel()

	got := newHarness(t).run("archive", "stats", "--channel", "general")

	if got.code != 1 {
		t.Fatalf("exit = %d", got.code)
	}

	if !strings.Contains(got.stderr, "unknown flag --channel") {
		t.Fatalf("stderr = %q", got.stderr)
	}
}

func TestVersion(t *testing.T) {
	t.Parallel()

	got := newHarness(t).run("version")

	if got.code != 0 {
		t.Fatalf("exit = %d", got.code)
	}

	if !strings.HasPrefix(got.stdout, "slack-browser ") {
		t.Fatalf("stdout = %q", got.stdout)
	}
}
