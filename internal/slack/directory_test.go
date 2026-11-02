package slack_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/slack"
)

// directoryClient is the slice of the API the directory uses, answered from memory.
type directoryClient struct {
	users    []slack.User
	channels []slack.Channel
	dms      []slack.Channel
	groups   []slack.Usergroup
	failing  bool
}

func (c directoryClient) ListUsers() ([]slack.User, error) {
	if c.failing {
		return nil, errDirectory
	}

	return c.users, nil
}

func (c directoryClient) ListAllChannels() ([]slack.Channel, error) {
	if c.failing {
		return nil, errDirectory
	}

	return c.channels, nil
}

func (c directoryClient) ListDMChannels() ([]slack.Channel, error) { return c.dms, nil }

func (c directoryClient) ListUsergroups() ([]slack.Usergroup, error) { return c.groups, nil }

var errDirectory = &directoryError{}

type directoryError struct{}

func (*directoryError) Error() string { return "slack is down" }

func TestFetchDirectoryFilesWhatSlackSaid(t *testing.T) {
	t.Parallel()

	directory, err := slack.FetchDirectory(directoryClient{
		users: []slack.User{
			{ID: "U01ANA", Name: "ana", DisplayName: "Ana Ruiz"},
			{ID: "U02MICHEL", Name: "michel"},
		},
		channels: []slack.Channel{
			{ID: "C01GENERAL", Name: "general"},
			{ID: "C02LEADS", Name: "leads", IsPrivate: true},
		},
		dms:    []slack.Channel{{ID: "D01ANA", IsIM: true, User: "U01ANA"}},
		groups: []slack.Usergroup{{ID: "S01CORE", Handle: "core"}},
	}, "T01ACME", time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("FetchDirectory: %v", err)
	}

	if directory.Version != 1 || directory.TeamID != "T01ACME" {
		t.Fatalf("directory = %+v", directory)
	}

	// The DM appears in both listings once Slack adds it to the channel list too, and
	// the id decides what the registry keeps.
	if len(directory.Channels) != 3 {
		t.Fatalf("channels = %d", len(directory.Channels))
	}

	if directory.DMCount == nil || *directory.DMCount != 1 {
		t.Fatalf("dm_count = %v", directory.DMCount)
	}

	// The label a person reads comes from the display name, not the handle.
	if got := directory.User("U01ANA"); got != "Ana Ruiz" {
		t.Fatalf("User = %q", got)
	}

	if got := directory.UserByHandle("ana"); got == nil || got.ID != "U01ANA" {
		t.Fatalf("UserByHandle = %+v", got)
	}

	if got := directory.Channel("C02LEADS"); got == nil || got.Kind != slack.KindPrivate {
		t.Fatalf("Channel = %+v", got)
	}
}

func TestFetchDirectoryFailsOnlyWhenNothingAnswered(t *testing.T) {
	t.Parallel()

	// A directory that answers nothing would render every id raw, so it is an error
	// rather than an empty registry.
	if _, err := slack.FetchDirectory(directoryClient{failing: true}, "T01ACME", time.Now()); err == nil {
		t.Fatal("an empty directory was accepted")
	}
}

func TestUserByReferenceRefusesAnAmbiguousName(t *testing.T) {
	t.Parallel()

	directory := &slack.Directory{Users: []slack.DirectoryUser{
		{ID: "U01ANA", Name: "ana", DisplayName: "Ana Ruiz"},
		{ID: "U02ANA", Name: "ana2", RealName: "Ana Ruiz"},
	}}

	if got := directory.UserByReference("Ana Ruiz"); got != nil {
		t.Fatalf("a name two people share resolved to %+v", got)
	}

	if got := directory.UserByReference("@u01ana"); got == nil || got.ID != "U01ANA" {
		t.Fatalf("an id did not resolve: %+v", got)
	}
}

func TestDirectoryStoreRoundTrip(t *testing.T) {
	t.Parallel()

	store := slack.NewDirectoryStore(t.TempDir())

	directory := &slack.Directory{
		Version: 1, TeamID: "T01ACME", SavedAt: "2026-11-02T10:00:00.000Z",
		Users:    []slack.DirectoryUser{{ID: "U01ANA", Name: "ana"}},
		Channels: []slack.DirectoryChannel{{ID: "C01GENERAL", Kind: slack.KindChannel, Name: "general"}},
	}

	path, err := store.Write(directory)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %#o, want 0600", info.Mode().Perm())
	}

	loaded, err := store.Read("T01ACME")
	if err != nil || loaded == nil {
		t.Fatalf("Read = %+v, %v", loaded, err)
	}

	if loaded.Channels[0].Name != "general" || loaded.Users[0].Name != "ana" {
		t.Fatalf("loaded = %+v", loaded)
	}

	// A directory is a cache: a file that does not parse is no cache, not an error.
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "directory.json"), []byte("{oops"), 0o600); err != nil {
		t.Fatalf("writing a broken directory: %v", err)
	}

	if loaded, err := store.Read("T01ACME"); err != nil || loaded != nil {
		t.Fatalf("a broken cache answered %+v, %v", loaded, err)
	}
}

func TestLoadDirectoryFallsBackToTheStaleCopy(t *testing.T) {
	t.Parallel()

	store := slack.NewDirectoryStore(t.TempDir())

	stale := &slack.Directory{
		Version: 1, TeamID: "T01ACME", SavedAt: "2020-01-01T00:00:00.000Z",
		Users:    []slack.DirectoryUser{{ID: "U01ANA", Name: "ana"}},
		Channels: []slack.DirectoryChannel{},
	}

	if _, err := store.Write(stale); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// An old name beats a raw id, so a failed refresh answers the copy on disk.
	loaded, err := slack.LoadDirectory(slack.LoadOptions{
		Client: directoryClient{failing: true}, TeamID: "T01ACME", Store: store,
	})
	if err != nil {
		t.Fatalf("LoadDirectory: %v", err)
	}

	if loaded == nil || len(loaded.Users) != 1 {
		t.Fatalf("loaded = %+v", loaded)
	}
}
