package archive_test

import (
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/archive"
)

// These rules decide whether a read is answered from disk or from Slack, and getting
// one wrong is invisible: the reader sees a plausible answer that is silently older
// than the conversation, or a channel that looks empty because nobody ever synced it.

func seededStore(t *testing.T, records []archive.ArchivedMessage) *archive.Store {
	t.Helper()

	store, _ := newStore(t)

	if err := store.Append("T000000001", "C01GENERAL", records); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	return store
}

func record(ts, text string) archive.ArchivedMessage {
	return archive.ArchivedMessage{TS: ts, Chan: "C01GENERAL", User: "U01ANA", Text: &text}
}

func reply(ts, threadTS, text string) archive.ArchivedMessage {
	message := record(ts, text)
	message.ThreadTS = threadTS

	return message
}

func TestLocalHistoryServesTheNewestWindowOldestFirst(t *testing.T) {
	t.Parallel()

	store := seededStore(t, []archive.ArchivedMessage{
		record("1793200000.000100", "one"),
		record("1793200100.000100", "two"),
		record("1793200200.000100", "three"),
	})

	messages, err := archive.LocalHistory(archive.LocalHistoryArgs{
		Store: store, TeamID: "T000000001", ChannelID: "C01GENERAL", Limit: 2,
	})
	if err != nil {
		t.Fatalf("LocalHistory: %v", err)
	}

	// The newest two, in reading order: `limit` means "the last N", not "the first N".
	if len(messages) != 2 || messages[0].Text != "two" || messages[1].Text != "three" {
		t.Fatalf("messages = %+v", messages)
	}
}

func TestLocalHistoryLeavesOutWhatIsNotChannelHistory(t *testing.T) {
	t.Parallel()

	deleted := record("1793200200.000100", "deleted")
	deleted.DeletedAt = "2026-11-02T10:00:00.000Z"

	searched := record("1793200300.000100", "found by search")
	searched.Source = "search"

	store := seededStore(t, []archive.ArchivedMessage{
		record("1793200000.000100", "the only channel message"),
		reply("1793200100.000100", "1793200000.000100", "a thread reply"),
		deleted,
		searched,
	})

	messages, err := archive.LocalHistory(archive.LocalHistoryArgs{
		Store: store, TeamID: "T000000001", ChannelID: "C01GENERAL", Limit: 50,
	})
	if err != nil {
		t.Fatalf("LocalHistory: %v", err)
	}

	// A reply filed as history would repeat the thread in the timeline, a deleted
	// message would resurrect, and a search hit is not a window at all.
	if len(messages) != 1 || messages[0].Text != "the only channel message" {
		t.Fatalf("messages = %+v", messages)
	}
}

func TestLocalHistoryDefersToSlackWhenTheWindowIsOlderThanTheArchive(t *testing.T) {
	t.Parallel()

	store := seededStore(t, []archive.ArchivedMessage{record("1793200500.000100", "recent")})

	// Asking for what came before the oldest stored message: the archive cannot tell
	// a quiet channel from one it never read that far back.
	messages, err := archive.LocalHistory(archive.LocalHistoryArgs{
		Store: store, TeamID: "T000000001", ChannelID: "C01GENERAL",
		Limit: 20, Latest: "1793200000.000000", Synced: true,
	})
	if err != nil {
		t.Fatalf("LocalHistory: %v", err)
	}

	if messages != nil {
		t.Fatalf("messages = %+v, want nil so Slack is asked", messages)
	}
}

func TestLocalHistoryTellsAQuietChannelFromAnUnknownOne(t *testing.T) {
	t.Parallel()

	store := seededStore(t, nil)

	// Never synced: nothing is known, so Slack decides.
	unknown, err := archive.LocalHistory(archive.LocalHistoryArgs{
		Store: store, TeamID: "T000000001", ChannelID: "C01GENERAL", Limit: 20,
	})
	if err != nil {
		t.Fatalf("LocalHistory: %v", err)
	}

	if unknown != nil {
		t.Fatalf("an unsynced channel answered %+v", unknown)
	}

	// Synced and empty: an empty answer is the answer, and asking Slack again would
	// cost a call to hear the same thing.
	quiet, err := archive.LocalHistory(archive.LocalHistoryArgs{
		Store: store, TeamID: "T000000001", ChannelID: "C01GENERAL", Limit: 20, Synced: true,
	})
	if err != nil {
		t.Fatalf("LocalHistory: %v", err)
	}

	if quiet == nil || len(quiet) != 0 {
		t.Fatalf("a quiet channel answered %+v, want an empty list", quiet)
	}
}

func TestLocalHistoryWindow(t *testing.T) {
	t.Parallel()

	store := seededStore(t, []archive.ArchivedMessage{
		record("1793200000.000100", "one"),
		record("1793200100.000100", "two"),
		record("1793200200.000100", "three"),
	})

	messages, err := archive.LocalHistory(archive.LocalHistoryArgs{
		Store: store, TeamID: "T000000001", ChannelID: "C01GENERAL", Limit: 20,
		Oldest: "1793200000.000100", Latest: "1793200200.000100",
	})
	if err != nil {
		t.Fatalf("LocalHistory: %v", err)
	}

	// Both ends are exclusive, which is what Slack's own oldest/latest mean.
	if len(messages) != 1 || messages[0].Text != "two" {
		t.Fatalf("messages = %+v", messages)
	}
}

func TestLocalThreadNeedsTheWholeThread(t *testing.T) {
	t.Parallel()

	two := 2
	zero := 0

	parent := record("1793200000.000100", "parent")
	parent.ReplyCount = &two

	complete := seededStore(t, []archive.ArchivedMessage{
		parent,
		reply("1793200100.000100", "1793200000.000100", "first"),
		reply("1793200200.000100", "1793200000.000100", "second"),
	})

	messages, err := archive.LocalThread(archive.LocalThreadArgs{
		Store: complete, TeamID: "T000000001", ChannelID: "C01GENERAL", ThreadTS: "1793200000.000100",
	})
	if err != nil {
		t.Fatalf("LocalThread: %v", err)
	}

	if len(messages) != 3 || messages[0].Text != "parent" || messages[2].Text != "second" {
		t.Fatalf("messages = %+v", messages)
	}

	// One reply short: serving this would look like the conversation ended there, so
	// Slack is asked instead.
	partial := seededStore(t, []archive.ArchivedMessage{
		parent,
		reply("1793200100.000100", "1793200000.000100", "first"),
	})

	half, err := archive.LocalThread(archive.LocalThreadArgs{
		Store: partial, TeamID: "T000000001", ChannelID: "C01GENERAL", ThreadTS: "1793200000.000100",
	})
	if err != nil {
		t.Fatalf("LocalThread: %v", err)
	}

	if half != nil {
		t.Fatalf("a half thread answered %+v", half)
	}

	// A parent with no reply count is a message Slack said nothing about, which is not
	// a thread of zero: the archive cannot answer it either way.
	unanswered := record("1793200000.000100", "parent")
	unanswered.ReplyCount = &zero

	quiet := seededStore(t, []archive.ArchivedMessage{unanswered})

	empty, err := archive.LocalThread(archive.LocalThreadArgs{
		Store: quiet, TeamID: "T000000001", ChannelID: "C01GENERAL", ThreadTS: "1793200000.000100",
	})
	if err != nil {
		t.Fatalf("LocalThread: %v", err)
	}

	if len(empty) != 1 {
		t.Fatalf("a thread of zero answered %+v", empty)
	}

	unknown := seededStore(t, []archive.ArchivedMessage{record("1793200000.000100", "parent")})

	none, err := archive.LocalThread(archive.LocalThreadArgs{
		Store: unknown, TeamID: "T000000001", ChannelID: "C01GENERAL", ThreadTS: "1793200000.000100",
	})
	if err != nil {
		t.Fatalf("LocalThread: %v", err)
	}

	if none != nil {
		t.Fatalf("a thread with no reply count answered %+v", none)
	}
}

func TestLocalThreadSkipsDeletedRepliesAndHonoursTheLimit(t *testing.T) {
	t.Parallel()

	// Slack decrements reply_count when a reply is deleted, so a channel that has seen
	// the deletion carries the smaller number. One is the whole thread here.
	one := 1
	parent := record("1793200000.000100", "parent")
	parent.ReplyCount = &one

	gone := reply("1793200100.000100", "1793200000.000100", "gone")
	gone.DeletedAt = "2026-11-02T10:00:00.000Z"

	store := seededStore(t, []archive.ArchivedMessage{
		parent,
		gone,
		reply("1793200200.000100", "1793200000.000100", "kept"),
	})

	messages, err := archive.LocalThread(archive.LocalThreadArgs{
		Store: store, TeamID: "T000000001", ChannelID: "C01GENERAL",
		Limit: 2, ThreadTS: "1793200000.000100",
	})
	if err != nil {
		t.Fatalf("LocalThread: %v", err)
	}

	// The deleted reply is not part of the thread any more, and the limit still counts
	// what is left rather than what was.
	if len(messages) != 2 || messages[1].Text != "kept" {
		t.Fatalf("messages = %+v", messages)
	}
}
