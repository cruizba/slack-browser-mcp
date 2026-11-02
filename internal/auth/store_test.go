package auth_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/auth"
	"github.com/cruizba/slack-browser-mcp/internal/config"
	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

func envWith(values map[string]string) platform.Env {
	return platform.Env{
		Getenv:    func(name string) string { return values[name] },
		LookupEnv: func(name string) (string, bool) { value, ok := values[name]; return value, ok },
		Home:      "/home/ana",
		OS:        "linux",
	}
}

type fixture struct {
	store    *auth.Store
	settings *config.Store
	dir      string
}

func newFixture(t *testing.T, values map[string]string) fixture {
	t.Helper()

	root := t.TempDir()
	dir := filepath.Join(root, "accounts")

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("creating the account directory: %v", err)
	}

	settings := config.NewStore(filepath.Join(root, "config.json"))

	return fixture{store: auth.NewStore(dir, settings, envWith(values)), settings: settings, dir: dir}
}

func (f fixture) write(t *testing.T, account auth.Account) {
	t.Helper()

	raw, err := json.Marshal(account)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}

	file := filepath.Join(f.dir, account.TeamID+".json")
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatalf("writing %s: %v", file, err)
	}

	if err := os.Chmod(file, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
}

func account(teamID, name, domain string) auth.Account {
	return auth.Account{
		TeamID:      teamID,
		TeamName:    name,
		TeamDomain:  domain,
		User:        "ana",
		UserID:      "U01ANA",
		Token:       "xoxc-1234567890-abcdef",
		DCookie:     "xoxd-abcdef%2Bghij",
		DCookieName: "d",
		SavedAt:     "2026-10-20T15:00:00.000Z",
	}
}

func TestListAndGet(t *testing.T) {
	t.Parallel()

	f := newFixture(t, nil)
	f.write(t, account("T01ACME", "Acme", "acme"))

	accounts, err := f.store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(accounts) != 1 {
		t.Fatalf("List() returned %d accounts", len(accounts))
	}

	summary := accounts[0]
	if summary.TeamID != "T01ACME" || summary.TeamName != "Acme" || summary.User != "ana" {
		t.Fatalf("summary = %+v", summary)
	}

	loaded, err := f.store.Get("T01ACME")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if loaded.Token != "xoxc-1234567890-abcdef" || loaded.DCookie != "xoxd-abcdef%2Bghij" {
		t.Fatalf("the secrets did not round-trip: %+v", loaded)
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()

	f := newFixture(t, nil)
	f.write(t, account("T01ACME", "Acme", "acme"))

	for _, reference := range []string{"T01ACME", "acme", "Acme", "ACM"} {
		resolved, err := f.store.Resolve(reference)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", reference, err)
		}

		if resolved.TeamID != "T01ACME" {
			t.Fatalf("Resolve(%q) = %s", reference, resolved.TeamID)
		}
	}

	f.write(t, account("T02VIDEO", "Acme Video", "video"))

	if _, err := f.store.Resolve("ac"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("Resolve(ac) = %v, want an ambiguity", err)
	}
}

func TestResolveWithoutAnythingStored(t *testing.T) {
	t.Parallel()

	f := newFixture(t, nil)

	_, err := f.store.Resolve("")
	if err == nil || !strings.Contains(err.Error(), "No Slack session stored yet") {
		t.Fatalf("Resolve() = %v", err)
	}

	accounts, err := f.store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(accounts) != 0 {
		t.Fatalf("List() on an empty directory = %v", accounts)
	}
}

func TestDefaultWithNothingStored(t *testing.T) {
	t.Parallel()

	fallback, err := newFixture(t, nil).store.Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}

	if fallback != nil {
		t.Fatalf("Default() = %+v", fallback)
	}
}

func TestDefaultIsTheOnlySession(t *testing.T) {
	t.Parallel()

	f := newFixture(t, nil)
	f.write(t, account("T01ACME", "Acme", "acme"))

	fallback, err := f.store.Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}

	if fallback == nil || fallback.TeamID != "T01ACME" || fallback.Source != auth.SourceOnly {
		t.Fatalf("Default() = %+v", fallback)
	}
}

func TestDefaultPrefersTheSavedChoice(t *testing.T) {
	t.Parallel()

	f := newFixture(t, nil)
	f.write(t, account("T01ACME", "Acme", "acme"))
	f.write(t, account("T02VIDEO", "Acme Video", "video"))

	if _, err := f.settings.SetDefaultWorkspace(new("T02VIDEO")); err != nil {
		t.Fatalf("SetDefaultWorkspace: %v", err)
	}

	fallback, err := f.store.Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}

	if fallback == nil || fallback.TeamID != "T02VIDEO" || fallback.Source != auth.SourceConfig {
		t.Fatalf("Default() = %+v", fallback)
	}
}

func TestDefaultPrefersTheEnvironment(t *testing.T) {
	t.Parallel()

	f := newFixture(t, map[string]string{auth.TeamEnv: "video"})
	f.write(t, account("T01ACME", "Acme", "acme"))
	f.write(t, account("T02VIDEO", "Acme Video", "video"))

	if _, err := f.settings.SetDefaultWorkspace(new("T01ACME")); err != nil {
		t.Fatalf("SetDefaultWorkspace: %v", err)
	}

	fallback, err := f.store.Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}

	if fallback == nil || fallback.TeamID != "T02VIDEO" || fallback.Source != auth.SourceEnvironment {
		t.Fatalf("Default() = %+v", fallback)
	}
}

func TestDefaultIsAmbiguousWithTwoSessions(t *testing.T) {
	t.Parallel()

	f := newFixture(t, nil)
	f.write(t, account("T01ACME", "Acme", "acme"))
	f.write(t, account("T02VIDEO", "Acme Video", "video"))

	fallback, err := f.store.Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}

	if fallback != nil {
		t.Fatalf("Default() = %+v, want nil", fallback)
	}
}

func TestBrokenFilesAreNamed(t *testing.T) {
	t.Parallel()

	f := newFixture(t, nil)
	f.write(t, account("T03BROKEN", "Broken", "broken"))

	file := filepath.Join(f.dir, "T03BROKEN.json")
	if err := os.WriteFile(file, []byte(`{"team_id": "T03BROKEN",`), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	if _, err := f.store.Get("T03BROKEN"); err == nil || !strings.Contains(err.Error(), file) {
		t.Fatalf("a broken file was not named: %v", err)
	}
}

func TestWidePermissionsAreRefused(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("Windows has no mode bits; internal/platform explains what it does instead")
	}

	f := newFixture(t, nil)
	f.write(t, account("T01ACME", "Acme", "acme"))

	file := filepath.Join(f.dir, "T01ACME.json")
	if err := os.Chmod(file, 0o644); err != nil { //nolint:gosec // the point is to widen it: the store has to refuse what it finds
		t.Fatalf("chmod: %v", err)
	}

	if _, err := f.store.List(); err == nil || !strings.Contains(err.Error(), "permissions are 644") {
		t.Fatalf("List() = %v, want a refusal naming the mode", err)
	}

	if err := os.Chmod(file, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if accounts, err := f.store.List(); err != nil || len(accounts) != 1 {
		t.Fatalf("List() after chmod = %v, %v", accounts, err)
	}
}

func TestInvalidTeamIDIsRefused(t *testing.T) {
	t.Parallel()

	f := newFixture(t, nil)

	if _, err := f.store.Get("../escape"); err == nil || !strings.Contains(err.Error(), "Invalid team id") {
		t.Fatalf("Get(../escape) = %v", err)
	}
}
