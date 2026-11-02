//go:build integration

package difftest_test

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/difftest"
	"github.com/cruizba/slack-browser-mcp/internal/fakeapi"
)

var (
	updateGoldens = flag.Bool("update", false,
		"re-record the goldens from this build; review the diff before committing")
	goBinary string
)

func TestMain(m *testing.M) {
	flag.Parse()

	dir, err := os.MkdirTemp("", "difftest-bin")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating a build directory: %v\n", err)
		os.Exit(1)
	}

	goBinary = filepath.Join(dir, "slack-browser")

	build := exec.CommandContext(context.Background(), "go", "build", //nolint:gosec // the harness exists to launch the build under test
		"-ldflags", buildInfoFlags(), "-o", goBinary, "./cmd/slack-browser")
	build.Dir = "../.."

	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building the Go binary: %v\n%s", err, out)
		discardDir(dir)
		os.Exit(1)
	}

	code := m.Run()

	discardDir(dir)
	os.Exit(code)
}

// buildInfoFlags stamps the build the way the Makefile and goreleaser do: the version
// from the tag this checkout stands on, and where git says the commit sits. The
// goldens normalize the number, so what matters is that it is stamped at all.
func buildInfoFlags() string {
	const pkg = "github.com/cruizba/slack-browser-mcp/internal/buildinfo"

	describe := gitAt("..", "describe", "--tags", "--always")
	version := strings.TrimPrefix(describe, "v")

	if version == "" {
		version = "0.0.0"
	}

	flags := []string{"-X", pkg + ".Version=" + version}

	if describe != "" {
		flags = append(flags, "-X", pkg+".Describe="+describe)

		if gitAt("..", "status", "--porcelain") != "" {
			flags = append(flags, "-X", pkg+".Dirty=1")
		}
	}

	return strings.Join(flags, " ")
}

// gitAt answers a git question, treating a failing command as no answer at all.
func gitAt(dir string, args ...string) string {
	command := exec.CommandContext(context.Background(), "git", args...) //nolint:gosec // fixed subcommands, no input
	command.Dir = dir

	out, err := command.Output()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(out))
}

func discardDir(dir string) {
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(os.Stderr, "removing %s: %v\n", dir, err)
	}
}

// A session seeded into the store, matching testdata/live/auth.test.json. The
// token shape is what the store validates, so a placeholder has to look real.
const seededAccount = `{
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

const seededSettings = `{"default_workspace":"T000000001"}`

const mcpHandshake = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"difftest","version":"0"}}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}
`

type scenario struct {
	name  string
	args  []string
	stdin string
}

var scenarios = []scenario{
	{name: "help", args: []string{"--help"}},
	{name: "version", args: []string{"version"}},
	{name: "accounts", args: []string{"accounts"}},
	{name: "status", args: []string{"status"}},
	{name: "workspace-list", args: []string{"workspace", "list"}},
	{name: "archive-stats", args: []string{"archive", "stats"}},
	{name: "read-self-dm", args: []string{"read", "me", "--limit", "3"}},
	{name: "mcp-tools", args: []string{"serve"}, stdin: mcpHandshake},
}

type build struct {
	label string
	cmd   string
	args  []string
}

type run struct {
	root       string
	env        []string
	normalizer difftest.Normalizer
}

func goBuild() build {
	return build{label: "go", cmd: goBinary}
}

func startFake(t *testing.T) *httptest.Server {
	t.Helper()

	server, err := fakeapi.New(filepath.Join("..", "..", "testdata", "live"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("loading fixtures: %v", err)
	}

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	return httpServer
}

func prepare(t *testing.T, fake string) run {
	t.Helper()

	root := t.TempDir()
	home := filepath.Join(root, "home")
	config := filepath.Join(root, "config")

	for _, dir := range []string{home, filepath.Join(config, "accounts"), filepath.Join(root, "data")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}
	}

	if err := os.WriteFile(filepath.Join(config, "accounts", "T000000001.json"), []byte(seededAccount), 0o600); err != nil {
		t.Fatalf("seeding the session: %v", err)
	}

	if err := os.WriteFile(filepath.Join(config, "config.json"), []byte(seededSettings), 0o600); err != nil {
		t.Fatalf("seeding the settings: %v", err)
	}

	env := append(os.Environ(),
		"HOME="+home,
		"XDG_DATA_HOME="+filepath.Join(root, "data"),
		"SLACK_BROWSER_MCP_CONFIG_DIR="+config,
		"SLACK_BROWSER_API_BASE="+fake+"/api",
		"SLACK_BROWSER_PACE_MS=0",
		"SLACK_BROWSER_MCP_NO_AUTOSYNC=1",
	)

	return run{
		root: root,
		env:  env,
		normalizer: difftest.Normalizer{
			Root: root,
			Home: home,
			Addr: fake,
		},
	}
}

func (r run) execute(t *testing.T, b build, sc scenario) difftest.Result {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	args := append(append([]string{}, b.args...), sc.args...)

	cmd := exec.CommandContext(ctx, b.cmd, args...) //nolint:gosec // the harness exists to launch the build under test
	cmd.Env = r.env
	cmd.Stdin = strings.NewReader(sc.stdin)

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := difftest.Result{
		Stdout: r.normalizer.Apply(stdout.String()),
		Stderr: r.normalizer.Apply(stderr.String()),
	}

	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		result.Exit = exitErr.ExitCode()

		return result
	}

	if err != nil {
		t.Fatalf("running %s %v: %v", b.label, sc.args, err)
	}

	return result
}

func goldenPath(name string) string {
	return filepath.Join("testdata", "golden", name+".golden")
}

func TestDifferential(t *testing.T) {
	t.Parallel()

	fake := startFake(t)

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()

			attempt := prepare(t, fake.URL).execute(t, goBuild(), sc)

			if *updateGoldens {
				if err := os.MkdirAll(filepath.Dir(goldenPath(sc.name)), 0o750); err != nil {
					t.Fatalf("creating the golden directory: %v", err)
				}

				if err := os.WriteFile(goldenPath(sc.name), []byte(attempt.Render()), 0o600); err != nil {
					t.Fatalf("writing the golden: %v", err)
				}

				return
			}

			raw, err := os.ReadFile(goldenPath(sc.name))
			if err != nil {
				t.Fatalf("reading the golden (record one with -update): %v", err)
			}

			want, err := difftest.Parse(raw)
			if err != nil {
				t.Fatalf("parsing the golden: %v", err)
			}

			if diff := difftest.Diff(want.Render(), attempt.Render()); diff != "" {
				t.Errorf("the Go build no longer matches the recorded behaviour:\n%s", diff)
			}
		})
	}
}
