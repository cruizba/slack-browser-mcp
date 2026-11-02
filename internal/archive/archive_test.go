package archive_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/archive"
	"github.com/cruizba/slack-browser-mcp/internal/slack"
)

func newStore(t *testing.T) (*archive.Store, string) {
	t.Helper()

	root := filepath.Join(t.TempDir(), "archive")

	return archive.NewStore(root), root
}

func TestAppendAndReadFoldsToOneRecordPerTS(t *testing.T) {
	t.Parallel()

	store, _ := newStore(t)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

	first := archive.ToRecord(slack.Channel{ID: "C01RANDOM", Name: "random"},
		slack.Message{TS: "1792500000.000100", User: "U01ANA", Text: "hello"}, now, "")

	if err := store.Append("T01ACME", "C01RANDOM", []archive.ArchivedMessage{first}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	edited := first
	edited.Text = new("hello, edited")
	edited.EditedTS = "1792500100.000000"

	if err := store.Append("T01ACME", "C01RANDOM", []archive.ArchivedMessage{edited}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	records, err := store.Read("T01ACME", "C01RANDOM")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("Read() = %d records, want the last line to win", len(records))
	}

	if records[0].Body() != "hello, edited" || records[0].EditedTS != "1792500100.000000" {
		t.Fatalf("the later line did not win: %+v", records[0])
	}
}

func TestDeletedAtDoesNotOutliveAConfirmation(t *testing.T) {
	t.Parallel()

	store, _ := newStore(t)
	now := time.Now()

	record := archive.ToRecord(slack.Channel{ID: "C01RANDOM", Name: "random"},
		slack.Message{TS: "1792500000.000100", Text: "hi"}, now, "")

	deleted := record
	deleted.DeletedAt = now.UTC().Format(time.RFC3339)

	revived := record

	if err := store.Append("T01ACME", "C01RANDOM", []archive.ArchivedMessage{record, deleted, revived}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	records, _ := store.Read("T01ACME", "C01RANDOM")
	if len(records) != 1 || records[0].DeletedAt != "" {
		t.Fatalf("a later line without deleted_at did not clear it: %+v", records)
	}
}

func TestTornLineCostsOnlyThatLine(t *testing.T) {
	t.Parallel()

	store, root := newStore(t)

	if err := os.MkdirAll(filepath.Join(root, "T01ACME"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	body := `{"ts":"1792500000.000100","channel":"C01RANDOM","synced_at":"2026-11-02T00:00:00Z"}` + "\n" +
		`{"ts":"1792500060.000200","chan` + "\n"

	if err := os.WriteFile(filepath.Join(root, "T01ACME", "C01RANDOM.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	records, err := store.Read("T01ACME", "C01RANDOM")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("Read() = %d records, want the torn line dropped", len(records))
	}
}

func TestIndexStatsAndSearch(t *testing.T) {
	t.Parallel()

	_, root := newStore(t)
	index := archive.NewIndex(filepath.Join(filepath.Dir(root), "archive.sqlite"))

	defer func() { _ = index.Close() }()

	now := time.Now()
	channel := slack.Channel{ID: "C01RANDOM", Name: "random"}

	records := []archive.ArchivedMessage{
		archive.ToRecord(channel, slack.Message{TS: "1792500000.000100", User: "U01ANA", Text: "deploy the thing"}, now, ""),
		archive.ToRecord(channel, slack.Message{TS: "1792500060.000200", User: "U01ANA", Text: "the thing is deployed"}, now, ""),
	}

	if err := index.Upsert("T01ACME", records); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	stats, err := index.Stats("")
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	if stats.Messages != 2 || stats.Channels != 1 || stats.Oldest != "1792500000.000100" {
		t.Fatalf("Stats = %+v", stats)
	}

	hits, err := index.Search(archive.SearchParams{Query: "deploy"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(hits) != 1 {
		t.Fatalf("Search returned %d hits, want 1", len(hits))
	}
}

func TestFTSQueryRefusesSyntax(t *testing.T) {
	t.Parallel()

	for _, query := range []string{`*`, `"`, `and or not`, `-`} {
		if got := archive.ToFTSQuery(query); got != "" {
			t.Fatalf("ToFTSQuery(%q) = %q, want an empty query", query, got)
		}
	}

	if got := archive.ToFTSQuery("deploy  the   thing"); got != `"deploy" AND "the" AND "thing"` {
		t.Fatalf("ToFTSQuery = %q", got)
	}
}

func TestTombstoneKeepsTheWords(t *testing.T) {
	t.Parallel()

	store, root := newStore(t)
	index := archive.NewIndex(filepath.Join(filepath.Dir(root), "archive.sqlite"))

	defer func() { _ = index.Close() }()

	now := time.Now()
	record := archive.ToRecord(slack.Channel{ID: "C01RANDOM", Name: "random"},
		slack.Message{TS: "1792500000.000100", Text: "searchable sentence"}, now, "")

	if err := index.Upsert("T01ACME", []archive.ArchivedMessage{record}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	tombstone := archive.ArchivedMessage{
		TS:        record.TS,
		Chan:      record.Chan,
		Text:      nil,
		DeletedAt: now.UTC().Format(time.RFC3339),
		SyncedAt:  record.SyncedAt,
	}

	if err := index.Upsert("T01ACME", []archive.ArchivedMessage{tombstone}); err != nil {
		t.Fatalf("Upsert tombstone: %v", err)
	}

	hits, err := index.Search(archive.SearchParams{Query: "searchable", IncludeDeleted: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(hits) != 1 || !hits[0].Deleted || hits[0].Text != "searchable sentence" {
		t.Fatalf("the tombstone lost the text: %+v", hits)
	}

	_ = store
}

func TestSchemaIsWrittenOnFirstUse(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "archive")

	status, err := archive.EnsureSchema(root, func(string) {})
	if err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	if status.From != archive.SchemaVersion || len(status.Applied) > 0 {
		t.Fatalf("status = %+v", status)
	}

	if _, err := os.Stat(filepath.Join(root, "schema.json")); err != nil {
		t.Fatalf("no marker was written: %v", err)
	}
}

func TestSchemaMigratesAnOldArchive(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "archive")
	dir := filepath.Join(root, "T01ACME")

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Schema 1: attachments were bare names and nulls were written out.
	body := `{"ts":"1792500000.000100","channel":"C01RANDOM","files":["report.pdf"],"user":null,"synced_at":"2026-11-02T00:00:00Z"}` + "\n"

	if err := os.WriteFile(filepath.Join(dir, "C01RANDOM.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	status, err := archive.EnsureSchema(root, func(string) {})
	if err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	if len(status.Applied) != 1 || status.Applied[0] != 2 {
		t.Fatalf("status = %+v", status)
	}

	migrated, err := os.ReadFile(filepath.Join(dir, "C01RANDOM.jsonl")) //nolint:gosec // the path is this test's own temporary directory
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	want := `{"channel":"C01RANDOM","files":[{"name":"report.pdf"}],"synced_at":"2026-11-02T00:00:00Z","ts":"1792500000.000100"}` + "\n"

	if string(migrated) != want {
		t.Fatalf("migrated line:\n%s\nwant:\n%s", migrated, want)
	}
}

func TestSchemaRefusesANewerArchive(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "archive")

	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	marker := `{"version": 99, "updated_at": "2026-11-02T00:00:00Z"}`

	if err := os.WriteFile(filepath.Join(root, "schema.json"), []byte(marker), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := archive.EnsureSchema(root, func(string) {}); err == nil {
		t.Fatal("a newer archive was accepted")
	}
}

func TestLockIsExclusiveAndReleases(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "lock")

	first := archive.NewLock(dir)

	acquired, err := first.Acquire()
	if err != nil || !acquired {
		t.Fatalf("Acquire = %v, %v", acquired, err)
	}

	second := archive.NewLock(dir)

	if acquired, err := second.Acquire(); err != nil || acquired {
		t.Fatalf("a second lock was granted: %v, %v", acquired, err)
	}

	owner, err := second.Owner()
	if err != nil || owner == nil || owner.PID != os.Getpid() {
		t.Fatalf("Owner = %+v, %v", owner, err)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	if acquired, err := second.Acquire(); err != nil || !acquired {
		t.Fatalf("the lock could not be retaken: %v, %v", acquired, err)
	}

	_ = second.Release()
}

func TestFormatAge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ms   *int64
		want string
	}{
		{name: "never", want: "never synced"},
		{name: "just now", ms: new(int64(30_000)), want: "just now"},
		{name: "minutes", ms: new(int64(5 * 60_000)), want: "5 min ago"},
		{name: "hours", ms: new(int64(3 * 60 * 60_000)), want: "3 h ago"},
		{name: "days", ms: new(int64(72 * 60 * 60_000)), want: "3 d ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := archive.FormatAge(tt.ms); got != tt.want {
				t.Fatalf("FormatAge = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestChanged(t *testing.T) {
	t.Parallel()

	now := time.Now()
	channel := slack.Channel{ID: "C01RANDOM", Name: "random"}
	record := archive.ToRecord(channel, slack.Message{TS: "1792500000.000100", Text: "hi"}, now, "")

	if !archive.Changed(nil, record) {
		t.Fatal("a record with nothing cached is a change")
	}

	if archive.Changed(&record, record) {
		t.Fatal("an identical record is not a change")
	}

	renamed := record
	renamed.ChannelName = "general"

	if !archive.Changed(&record, renamed) {
		t.Fatal("a rename is a change")
	}

	fromSearch := record
	fromSearch.Source = "search"

	if !archive.Changed(&record, fromSearch) {
		t.Fatal("a change of origin is a change")
	}
}
