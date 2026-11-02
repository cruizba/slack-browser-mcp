package archive

import (
	"sort"

	"github.com/cruizba/slack-browser-mcp/internal/slack"
)

// LocalHistoryArgs is what a channel read asks the archive for.
type LocalHistoryArgs struct {
	Store     *Store
	TeamID    string
	ChannelID string
	Limit     int
	Oldest    string
	Latest    string
	// Synced says a sync has walked the channel, so finding nothing in it is an
	// answer too.
	Synced bool
}

// LocalHistory answers a channel's top-level history from disk, or nil when Slack
// has to be asked: the archive holds nothing for the channel and no sync has walked
// it either.
func LocalHistory(args LocalHistoryArgs) ([]slack.Message, error) {
	records, err := args.Store.Read(args.TeamID, args.ChannelID)
	if err != nil {
		return nil, err
	}

	top := topLevel(records)

	// A window that ends before the oldest record is not an answer: the archive cannot
	// tell a quiet channel from one it never read that far back, so Slack decides.
	floor := 0.0
	if len(top) > 0 {
		floor = numeric(top[0].TS)
	}

	if args.Latest != "" && numeric(args.Latest) <= floor && floor > 0 {
		return nil, nil
	}

	inWindow := window(top, args.Oldest, args.Latest)

	if len(inWindow) > 0 {
		if len(inWindow) > args.Limit {
			inWindow = inWindow[len(inWindow)-args.Limit:]
		}

		return fromRecords(inWindow), nil
	}

	// A quiet channel leaves no records behind; without this every read of it would go
	// to Slack only to hear the same empty answer.
	if len(top) == 0 && !args.Synced {
		return nil, nil
	}

	// Archived but outside the window: an empty answer, not a reason to ask Slack.
	return []slack.Message{}, nil
}

// topLevel is the channel's own history: conversations.history never returns thread
// replies, and a search hit is not a window, so neither belongs here.
func topLevel(records []ArchivedMessage) []ArchivedMessage {
	top := make([]ArchivedMessage, 0, len(records))

	for _, record := range records {
		if record.DeletedAt != "" || record.ThreadTS != "" || record.Source == "search" {
			continue
		}

		top = append(top, record)
	}

	return top
}

// window cuts a ts range out of the records and orders it oldest first.
func window(records []ArchivedMessage, oldest, latest string) []ArchivedMessage {
	from := numeric(oldest)
	to := numeric(latest)

	kept := make([]ArchivedMessage, 0, len(records))

	for _, record := range records {
		ts := numeric(record.TS)
		if oldest != "" && ts <= from {
			continue
		}

		if latest != "" && ts >= to {
			continue
		}

		kept = append(kept, record)
	}

	sort.SliceStable(kept, func(left, right int) bool {
		return numeric(kept[left].TS) < numeric(kept[right].TS)
	})

	return kept
}

// LocalThreadArgs is what a thread read asks the archive for.
type LocalThreadArgs struct {
	Store     *Store
	TeamID    string
	ChannelID string
	ThreadTS  string
	Limit     int
}

// LocalThread answers a whole thread, or nil while a single reply is missing: serving
// half a thread silently would look like the conversation ended there.
func LocalThread(args LocalThreadArgs) ([]slack.Message, error) {
	records, err := args.Store.Read(args.TeamID, args.ChannelID)
	if err != nil {
		return nil, err
	}

	parent := threadParent(records, args.ThreadTS)
	if parent == nil || parent.DeletedAt != "" || parent.ReplyCount == nil {
		return nil, nil
	}

	replies := threadReplies(records, args.ThreadTS)

	expected := 0
	if parent.ReplyCount != nil {
		expected = *parent.ReplyCount
	}

	// One missing reply is not a thread: serving half of it silently would look like
	// the conversation ended there.
	if len(replies) < expected {
		return nil, nil
	}

	thread := append([]ArchivedMessage{*parent}, replies...)

	if args.Limit > 0 && len(thread) > args.Limit {
		thread = thread[:args.Limit]
	}

	return fromRecords(thread), nil
}

func threadParent(records []ArchivedMessage, threadTS string) *ArchivedMessage {
	for index, record := range records {
		if record.TS == threadTS && record.Source != "search" {
			return &records[index]
		}
	}

	return nil
}

func threadReplies(records []ArchivedMessage, threadTS string) []ArchivedMessage {
	replies := make([]ArchivedMessage, 0, 8)

	for _, record := range records {
		if record.ThreadTS == threadTS && record.TS != threadTS && record.DeletedAt == "" {
			replies = append(replies, record)
		}
	}

	sort.SliceStable(replies, func(left, right int) bool {
		return numeric(replies[left].TS) < numeric(replies[right].TS)
	})

	return replies
}

func fromRecords(records []ArchivedMessage) []slack.Message {
	messages := make([]slack.Message, 0, len(records))

	for _, record := range records {
		messages = append(messages, FromRecord(record))
	}

	return messages
}
