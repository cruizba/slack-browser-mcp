package archive_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/archive"
	"github.com/cruizba/slack-browser-mcp/internal/slack"
)

// syncClient is a Slack with a history and a thread per parent, and a memory of what it
// was asked.
type syncClient struct {
	channels []slack.Channel
	history  func(params slack.HistoryParams) slack.HistoryPage
	replies  map[string]slack.HistoryPage
	files    map[string][]byte

	historyCalls []slack.HistoryParams
	threadCalls  []string
}

func (c *syncClient) ListMyChannels() ([]slack.Channel, error) { return c.channels, nil }

func (c *syncClient) History(params slack.HistoryParams) (slack.HistoryPage, error) {
	c.historyCalls = append(c.historyCalls, params)

	return c.history(params), nil
}

func (c *syncClient) RepliesPage(_, threadTS string, _ int, cursor string) (slack.HistoryPage, error) {
	if cursor == "" {
		c.threadCalls = append(c.threadCalls, threadTS)
	}

	page, known := c.replies[threadTS]
	if !known {
		return slack.HistoryPage{}, nil
	}

	return page, nil
}

func (c *syncClient) DownloadFile(file slack.File) ([]byte, error) {
	body, known := c.files[file.ID]
	if !known {
		return nil, errors.New("no such file")
	}

	return body, nil
}

// syncFixture is one workspace with one channel and a run that writes where a test can
// read it.
func syncFixture(t *testing.T) (*archive.Store, *archive.Index, archive.SyncOptions) {
	t.Helper()

	root := t.TempDir()
	store := archive.NewStore(filepath.Join(root, "archive"))
	index := archive.NewIndex(filepath.Join(root, "archive.sqlite"))

	t.Cleanup(func() { _ = index.Close() })

	return store, index, archive.SyncOptions{
		Now:        time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC),
		Deadline:   time.Minute,
		MaxThreads: 10,
	}
}

func message(ts, text string) slack.Message {
	return slack.Message{TS: ts, User: "U01ANA", Text: text}
}

func TestSyncBackfillsAndThenFindsNothingNew(t *testing.T) {
	t.Parallel()

	client := &syncClient{
		channels: []slack.Channel{{ID: "C01ACME", Name: "acme"}},
		history: func(slack.HistoryParams) slack.HistoryPage {
			return slack.HistoryPage{Messages: []slack.Message{
				message("1793200000.000200", "second"),
				message("1793200000.000100", "first"),
			}}
		},
	}

	store, index, options := syncFixture(t)

	report, err := archive.SyncAccount("T01ACME", client, store, index, options)
	if err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}

	if report.ChannelsSynced != 1 || report.MessagesAdded != 2 {
		t.Fatalf("report = %+v", report)
	}

	records, err := store.Read("T01ACME", "C01ACME")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(records) != 2 || records[0].Body() != "first" {
		t.Fatalf("records = %+v", records)
	}

	// The index is written in the same pass: a read must not need a reindex to find what
	// the sync just filed.
	hits, err := index.Search(archive.SearchParams{Query: "second"})
	if err != nil || len(hits) != 1 {
		t.Fatalf("Search = %v, %v", hits, err)
	}

	state, err := store.LoadState()
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	channel := state.Teams["T01ACME"].Channels["C01ACME"]

	if channel.NewestTS != "1793200000.000200" || channel.LastSync == "" {
		t.Fatalf("cursor = %+v", channel)
	}
}

// TestSyncTwiceAddsNothing is the property that makes a daily sync cheap: the second run
// with the same data appends nothing.
func TestSyncTwiceAddsNothing(t *testing.T) {
	t.Parallel()

	client := &syncClient{
		channels: []slack.Channel{{ID: "C01ACME", Name: "acme"}},
		history: func(slack.HistoryParams) slack.HistoryPage {
			return slack.HistoryPage{Messages: []slack.Message{
				message("1793200000.000200", "second"),
				message("1793200000.000100", "first"),
			}}
		},
	}

	store, index, options := syncFixture(t)

	if _, err := archive.SyncAccount("T01ACME", client, store, index, options); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}

	second, err := archive.SyncAccount("T01ACME", client, store, index, options)
	if err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}

	if second.MessagesAdded != 0 {
		t.Fatalf("the second run added %d messages", second.MessagesAdded)
	}
}

func TestSyncMarksWhatDisappearedAsDeleted(t *testing.T) {
	t.Parallel()

	client := &syncClient{
		channels: []slack.Channel{{ID: "C01ACME", Name: "acme"}},
		history: func(slack.HistoryParams) slack.HistoryPage {
			return slack.HistoryPage{Messages: []slack.Message{
				message("1793200000.000200", "second"),
				message("1793200000.000100", "first"),
			}}
		},
	}

	store, index, options := syncFixture(t)

	if _, err := archive.SyncAccount("T01ACME", client, store, index, options); err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}

	// One message is gone from Slack on the next pass.
	client.history = func(slack.HistoryParams) slack.HistoryPage {
		return slack.HistoryPage{Messages: []slack.Message{message("1793200000.000200", "second")}}
	}

	// The reconcile window is re-read once a day, so the deletion pass only runs when
	// that day has passed; an hour later it would still trust the previous window.
	options.Now = options.Now.Add(25 * time.Hour)
	options.ReconcileDays = 7

	report, err := archive.SyncAccount("T01ACME", client, store, index, options)
	if err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}

	if report.DeletionsMarked != 1 {
		t.Fatalf("report = %+v", report)
	}

	records, _ := store.Read("T01ACME", "C01ACME")

	// A deletion is a line with `deleted_at`, never a removed line: losing the fact that
	// a message existed would make the archive lie about history.
	if len(records) != 2 {
		t.Fatalf("records = %d", len(records))
	}

	if records[0].DeletedAt == "" || records[0].Body() != "first" {
		t.Fatalf("the deletion was not marked: %+v", records[0])
	}
}

func TestSyncKeepsTheCursorWhenAReadStoppedEarly(t *testing.T) {
	t.Parallel()

	client := &syncClient{
		channels: []slack.Channel{{ID: "C01ACME", Name: "acme"}},
		history: func(slack.HistoryParams) slack.HistoryPage {
			// More pages exist and there is no cursor to continue from: stopping is the
			// only honest answer, and it must not pass for the end of the channel.
			return slack.HistoryPage{Messages: []slack.Message{message("1793200000.000100", "first")}, HasMore: true}
		},
	}

	store, index, options := syncFixture(t)

	report, err := archive.SyncAccount("T01ACME", client, store, index, options)
	if err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}

	if !report.Truncated {
		t.Fatalf("a truncated read was not reported: %+v", report)
	}

	state, _ := store.LoadState()
	channel := state.Teams["T01ACME"].Channels["C01ACME"]

	// Stepping over the part it never fetched is how a gap becomes permanent.
	if channel.NewestTS != "" || channel.LastSync != "" {
		t.Fatalf("the cursor moved past a window it never read: %+v", channel)
	}

	if channel.OldestTS != "1793200000.000100" {
		t.Fatalf("oldest = %q", channel.OldestTS)
	}
}

func TestSyncDrainsThreadsAndStoresTheEnvelope(t *testing.T) {
	t.Parallel()

	parent := message("1793200000.000100", "the task")
	parent.ReplyCount = new(1)

	client := &syncClient{
		channels: []slack.Channel{{ID: "C01ACME", Name: "acme"}},
		history: func(slack.HistoryParams) slack.HistoryPage {
			return slack.HistoryPage{Messages: []slack.Message{parent}}
		},
		replies: map[string]slack.HistoryPage{
			"1793200000.000100": {Messages: []slack.Message{
				parent,
				{TS: "1793200000.000200", User: "U02MICHEL", ThreadTS: "1793200000.000100", Text: "accepted"},
			}},
		},
		files: map[string][]byte{
			"F01ENVELOPE": []byte("---\nv: 1\n---\n\nbody"),
		},
	}

	parent.Files = []slack.File{{ID: "F01ENVELOPE", Name: "forum-01J9Z7QK3M4N5P6R7S8T9V0W1X.md", Size: 100}}

	store, index, options := syncFixture(t)

	report, err := archive.SyncAccount("T01ACME", client, store, index, options)
	if err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}

	if report.ThreadsFetched != 1 || report.RepliesAdded != 1 {
		t.Fatalf("report = %+v", report)
	}

	records, _ := store.Read("T01ACME", "C01ACME")

	var found bool

	for _, record := range records {
		if record.TS == "1793200000.000100" {
			found = true

			// The envelope is copied into the record, which is what keeps
			// `inbox --offline` honest without a download on every read.
			if record.Forum == "" {
				t.Fatalf("the envelope was not kept: %+v", record)
			}
		}
	}

	if !found {
		t.Fatal("the parent was not filed")
	}

	// The thread was drained, so it is not queued for the next run.
	state, _ := store.LoadState()
	channel := state.Teams["T01ACME"].Channels["C01ACME"]

	if len(channel.PendingThread) != 0 {
		t.Fatalf("pending = %v", channel.PendingThread)
	}
}

func TestSyncRestrictedToOneChannel(t *testing.T) {
	t.Parallel()

	client := &syncClient{
		channels: []slack.Channel{
			{ID: "C01ACME", Name: "acme"},
			{ID: "C02OTHER", Name: "other"},
		},
		history: func(params slack.HistoryParams) slack.HistoryPage {
			return slack.HistoryPage{Messages: []slack.Message{message("1793200000.000100", params.Channel)}}
		},
	}

	store, index, options := syncFixture(t)
	options.Channels = []string{"C02OTHER"}

	report, err := archive.SyncAccount("T01ACME", client, store, index, options)
	if err != nil {
		t.Fatalf("SyncAccount: %v", err)
	}

	if report.ChannelsSeen != 1 || report.ChannelsSynced != 1 {
		t.Fatalf("report = %+v", report)
	}

	// Two calls: the incremental window and the reconcile one. Both are for the channel
	// the caller named, and neither touches the other.
	for _, call := range client.historyCalls {
		if call.Channel != "C02OTHER" {
			t.Fatalf("a channel outside the filter was read: %+v", call)
		}
	}
}
