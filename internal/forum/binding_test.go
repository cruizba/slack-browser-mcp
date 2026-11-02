package forum_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/forum"
)

// The forum file is the machine's own state: which channels it listens to, who it is,
// and which stale labels it has retired. Nothing else writes it, so the rules that
// matter are the ones an interrupted or repeated command can break — removing what is
// not there, binding the same channel twice, and reading back what was written.

func forumStore(t *testing.T) (*forum.Store, string) {
	t.Helper()

	file := filepath.Join(t.TempDir(), "forum.json")

	return forum.NewStore(file), file
}

func readForum(t *testing.T, file string) forum.File {
	t.Helper()

	content, err := os.ReadFile(file) //nolint:gosec // the path is this test's own temporary directory
	if err != nil {
		t.Fatalf("reading the forum file: %v", err)
	}

	var parsed forum.File
	if err := json.Unmarshal(content, &parsed); err != nil {
		t.Fatalf("the forum file is not JSON: %v", err)
	}

	return parsed
}

func TestBindAndUnbindAreIdempotent(t *testing.T) {
	t.Parallel()

	store, file := forumStore(t)
	binding := forum.Binding{
		Workspace: "T000000001", Channel: "C01FORUM", Name: "forum",
		SinceTS: "0", AddedAt: "2026-11-01T00:00:00.000Z",
	}

	added, err := store.Bind(binding)
	if err != nil || !added {
		t.Fatalf("Bind = %v, %v", added, err)
	}

	// Binding twice is what a setup script does, and it must not duplicate the entry.
	added, err = store.Bind(binding)
	if err != nil || added {
		t.Fatalf("the second Bind = %v, %v; want it to change nothing", added, err)
	}

	if got := readForum(t, file); len(got.Channels) != 1 {
		t.Fatalf("channels = %+v", got.Channels)
	}

	removed, err := store.Unbind("T000000001", "C01FORUM")
	if err != nil || !removed {
		t.Fatalf("Unbind = %v, %v", removed, err)
	}

	// Unbinding what is not bound answers "nothing changed" instead of failing, so a
	// cleanup script can run twice.
	removed, err = store.Unbind("T000000001", "C01FORUM")
	if err != nil || removed {
		t.Fatalf("the second Unbind = %v, %v", removed, err)
	}

	if got := readForum(t, file); len(got.Channels) != 0 {
		t.Fatalf("channels = %+v", got.Channels)
	}
}

func TestRegisterAndUnregisterForgetTheIdentity(t *testing.T) {
	t.Parallel()

	store, file := forumStore(t)

	identity := forum.StoredIdentity{Name: "builder", Host: "laptop", RegisteredAt: "2026-11-01T00:00:00.000Z"}

	if _, err := store.Register("agent", identity); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Unregistering retires the label on the way out: a name left unmarked would keep
	// collecting work this machine will never do, and the tasks would sit in an inbox
	// nobody reads.
	removed, err := store.Unregister("agent", "builder@laptop")
	if err != nil || !removed {
		t.Fatalf("Unregister = %v, %v", removed, err)
	}

	parsed := readForum(t, file)

	if _, present := parsed.Identities["agent"]; present {
		t.Fatalf("the identity is still there: %+v", parsed.Identities)
	}

	if len(parsed.Ignored) != 1 || parsed.Ignored[0] != "builder@laptop" {
		t.Fatalf("ignored = %v, want the label retired on the way out", parsed.Ignored)
	}
}

func TestIgnoreAndUnignore(t *testing.T) {
	t.Parallel()

	store, file := forumStore(t)

	added, err := store.Ignore("alice@old")
	if err != nil || !added {
		t.Fatalf("Ignore = %v, %v", added, err)
	}

	added, err = store.Ignore("alice@old")
	if err != nil || added {
		t.Fatalf("the second Ignore = %v, %v; want it to change nothing", added, err)
	}

	if got := readForum(t, file).Ignored; len(got) != 1 || got[0] != "alice@old" {
		t.Fatalf("ignored = %v", got)
	}

	removed, err := store.Unignore("alice@old")
	if err != nil || !removed {
		t.Fatalf("Unignore = %v, %v", removed, err)
	}

	if got := readForum(t, file).Ignored; len(got) != 0 {
		t.Fatalf("ignored = %v", got)
	}

	removed, err = store.Unignore("alice@old")
	if err != nil || removed {
		t.Fatalf("the second Unignore = %v, %v", removed, err)
	}
}

func TestTargetLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		target forum.Target
		want   string
	}{
		{forum.Target{Anyone: true}, "*"},
		{forum.Target{Name: "alice", Host: "laptop"}, "alice@laptop"},
		{forum.Target{Name: "alice"}, "alice"},
	}

	for _, test := range tests {
		if got := forum.TargetLabel(test.target); got != test.want {
			t.Fatalf("TargetLabel(%+v) = %q, want %q", test.target, got, test.want)
		}
	}
}

func TestBindingKeyAndLabel(t *testing.T) {
	t.Parallel()

	public := forum.Binding{Workspace: "T000000001", Channel: "C01FORUM", Name: "forum"}

	if got := forum.ChannelLabel(public); got != "#forum" {
		t.Fatalf("ChannelLabel = %q", got)
	}

	private := forum.Binding{Workspace: "T000000001", Channel: "C02LEADS", Name: "leads", Private: true}
	if got := forum.ChannelLabel(private); got != "private:leads" {
		t.Fatalf("ChannelLabel = %q", got)
	}

	// The key identifies one binding for the whole file, so two channels with the same
	// name on different workspaces cannot collide.
	if forum.BindingKey("T000000001", "C01FORUM") == forum.BindingKey("T000000002", "C01FORUM") {
		t.Fatal("two workspaces share a binding key")
	}

	if got := forum.NewStore(filepath.Join(t.TempDir(), "forum.json")).Path(); got == "" {
		t.Fatal("Path is empty")
	}
}
