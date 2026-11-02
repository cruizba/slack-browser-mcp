package mcp_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/config"
	"github.com/cruizba/slack-browser-mcp/internal/fakeapi"
	"github.com/cruizba/slack-browser-mcp/internal/mcp"
	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

// harness is one machine: a home with a stored session, and a fake Slack serving the
// fixtures recorded from the real API.
type harness struct {
	t     *testing.T
	deps  mcp.Deps
	fake  *httptest.Server
	paths config.Paths
}

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

func newHarness(t *testing.T) *harness {
	t.Helper()

	server, err := fakeapi.New(filepath.Join("..", "..", "testdata", "live"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("loading fixtures: %v", err)
	}

	// The recorded fixtures are reads: posting to a real channel was never part of the
	// capture. The fake answers the two writes a test needs, and delegates the rest.
	fake := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/chat.postMessage") {
			response.Header().Set("Content-Type", "application/json")

			_, _ = response.Write([]byte(`{"ok":true,"ts":"1793200000.000100","channel":"C000000002","message":{"text":"hello"}}`))

			return
		}

		server.Handler().ServeHTTP(response, request)
	}))
	t.Cleanup(fake.Close)

	root := t.TempDir()

	// The fake answers `/api/<method>`, and the client appends the method to whatever
	// base it is given.
	base := fake.URL + "/api/"

	env := platform.Env{
		Getenv: func(name string) string {
			if name == "SLACK_BROWSER_API_BASE" {
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
		Home: root,
		OS:   "linux",
	}

	paths := config.Resolve(env)

	if err := os.MkdirAll(paths.Accounts, 0o700); err != nil {
		t.Fatalf("creating the account directory: %v", err)
	}

	account := filepath.Join(paths.Accounts, "T000000001.json")
	if err := os.WriteFile(account, []byte(seededAccount), 0o600); err != nil {
		t.Fatalf("seeding the account: %v", err)
	}

	settings := `{"default_workspace":"T000000001"}`
	if err := os.WriteFile(paths.SettingsFile, []byte(settings), 0o600); err != nil {
		t.Fatalf("seeding the settings: %v", err)
	}

	return &harness{
		t: t,
		deps: mcp.Deps{
			Paths:   paths,
			Env:     env,
			Version: "0.8.0",
			BaseURL: base,
		},
		fake:  fake,
		paths: paths,
	}
}

// call runs one tool and answers its first text block.
func (h *harness) call(name string, arguments any) string {
	h.t.Helper()

	raw, err := json.Marshal(arguments)
	if err != nil {
		h.t.Fatalf("encoding the arguments: %v", err)
	}

	handler, known := mcp.Handlers(h.deps)[name]
	if !known {
		h.t.Fatalf("no handler for %s", name)
	}

	content, err := handler(context.Background(), raw)
	if err != nil {
		return "ERROR: " + err.Error()
	}

	if len(content) == 0 {
		h.t.Fatalf("%s answered nothing", name)
	}

	return content[0].Text
}

func TestCatalogueIsTheSurface(t *testing.T) {
	t.Parallel()

	instructions, tools, err := mcp.Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}

	if len(tools) != 13 {
		t.Fatalf("tools = %d, want the 13 the slack surface serves", len(tools))
	}

	if !strings.Contains(instructions, "post_message is the only tool with a side effect") {
		t.Fatalf("instructions do not carry the rule about writing:\n%s", instructions[:200])
	}

	// Every tool has a handler: a catalogue entry with no handler answers "not ported",
	// which is a bug nobody notices until an agent calls it.
	handlers := mcp.Handlers(mcp.Deps{})

	for _, tool := range tools {
		if _, known := handlers[tool.Name]; !known {
			t.Fatalf("%s has no handler", tool.Name)
		}
	}
}

func TestAuthStatus(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	text := h.call("auth_status", map[string]any{})

	if !strings.Contains(text, "ok  CodeURJC (T000000001) as carlos.ruiz [U000000001]") {
		t.Fatalf("auth_status = %q", text)
	}

	if !strings.Contains(text, "default: CodeURJC (T000000001) via config.json") {
		t.Fatalf("auth_status does not say where the default came from: %q", text)
	}
}

func TestListWorkspaces(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	text := h.call("list_workspaces", map[string]any{})

	if !strings.Contains(text, "* CodeURJC (T000000001)") {
		t.Fatalf("list_workspaces = %q", text)
	}

	if !strings.Contains(text, "stored: 1 workspace") {
		t.Fatalf("list_workspaces does not count: %q", text)
	}
}

func TestSetDefaultWorkspace(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	if text := h.call("set_default_workspace", map[string]any{"clear": true}); !strings.Contains(text, "cleared") {
		t.Fatalf("clear answered %q", text)
	}

	text := h.call("set_default_workspace", map[string]any{"workspace": "codeurjc"})
	if !strings.Contains(text, "default workspace: CodeURJC (T000000001)") {
		t.Fatalf("set answered %q", text)
	}
}

func TestReadMessagesFromSlackThenFromTheArchive(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	first := h.call("read_messages", map[string]any{"channel": "me", "limit": 3})

	if !strings.Contains(first, "messages, oldest first · from Slack") {
		t.Fatalf("the first read did not come from Slack:\n%s", first)
	}

	// The same read again answers from what the first one filed, and says how old it is.
	second := h.call("read_messages", map[string]any{"channel": "me", "limit": 3})

	if !strings.Contains(second, "archive:") {
		t.Fatalf("the second read did not come from the archive:\n%s", second)
	}
}

func TestPostMessageIsADryRunUntilConfirmed(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	preview := h.call("post_message", map[string]any{"channel": "general", "text": "hello"})

	if !strings.HasPrefix(preview, "Nothing sent. Would post to CodeURJC · #general") {
		t.Fatalf("preview = %q", preview)
	}

	if !strings.Contains(preview, "Call again with confirm=true") {
		t.Fatalf("the preview does not say how to commit: %q", preview)
	}

	sent := h.call("post_message", map[string]any{"channel": "general", "text": "hello", "confirm": true})

	if !strings.HasPrefix(sent, "Sent to CodeURJC · #general") {
		t.Fatalf("send = %q", sent)
	}

	if !strings.Contains(sent, "Audit:") {
		t.Fatalf("the send was not audited: %q", sent)
	}

	// Every real send leaves a line in the audit log, which is the only record of what
	// went out.
	if _, err := os.Stat(h.paths.AuditLog); err != nil {
		t.Fatalf("no audit log was written: %v", err)
	}
}

func TestArchiveStatusBeforeAnythingIsSynced(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	text := h.call("archive_status", map[string]any{})

	if !strings.Contains(text, "No archive yet") {
		t.Fatalf("archive_status = %q", text)
	}
}

func TestUnknownArgumentsAreRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	// A tool is called with the arguments its schema promised; anything else is a bug
	// in the client and should not reach Slack.
	if text := h.call("read_messages", "not an object"); !strings.HasPrefix(text, "ERROR:") {
		t.Fatalf("bad arguments answered %q", text)
	}
}
