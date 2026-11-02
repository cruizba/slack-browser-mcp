package forum_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/archive"
	"github.com/cruizba/slack-browser-mcp/internal/forum"
)

// slackStamp is a Slack ts for a moment, so a test's clock and its messages agree.
func slackStamp(when time.Time) string {
	return strconv.FormatInt(when.Unix(), 10) + ".000100"
}

func ledgerArgs(messages []archive.ArchivedMessage, now time.Time) forum.LedgerArgs {
	return forum.LedgerArgs{
		Messages:  messages,
		TeamID:    "T01ACME",
		ChannelID: "C01FORUM",
		Roster: []forum.Peer{
			{From: "builder@laptop", Name: "builder", Host: "laptop"},
			{From: "reviewer@desk", Name: "reviewer", Host: "desk"},
			{From: "ana@laptop", Name: "ana", Host: "laptop"},
		},
		Me:     "ana@laptop",
		Limits: forum.DefaultLimits,
		Now:    now,
	}
}

func taskEnvelope(id, from, to, title string) *forum.Envelope {
	depth := 0

	return &forum.Envelope{
		V: 1, Kind: forum.KindTask, ID: id, From: from, To: to,
		Sent: "2026-11-02T10:00:00.000Z", Title: title,
		Body: "do the thing", HasBody: true, Depth: &depth,
	}
}

func replyEnvelope(id, kind, from, taskID string) *forum.Envelope {
	return &forum.Envelope{
		V: 1, Kind: kind, ID: id, From: from, Sent: "2026-11-02T10:05:00.000Z",
		Task: taskID, Body: "a note", HasBody: kind != forum.KindAccept,
	}
}

func TestLedgerFollowsTheWorkflow(t *testing.T) {
	t.Parallel()

	const taskID = "01J9Z7QK3M4N5P6R7S8T9V0W1X"

	now := time.Date(2026, 11, 2, 10, 10, 0, 0, time.UTC)
	start := now.Add(-10 * time.Minute)

	messages := []archive.ArchivedMessage{
		archivedUser(t, taskEnvelope(taskID, "ana@laptop", "builder@laptop", "Sync it"), slackStamp(start), "U01ANA"),
		archived(t, replyEnvelope("01J9Z7QK3M4N5P6R7S8T9V0W2X", forum.KindAccept, "builder@laptop", taskID), slackStamp(start.Add(2*time.Minute))),
		archived(t, replyEnvelope("01J9Z7QK3M4N5P6R7S8T9V0W3X", forum.KindResult, "builder@laptop", taskID), slackStamp(start.Add(4*time.Minute))),
	}

	ledger := forum.BuildLedger(ledgerArgs(messages, now))

	if len(ledger.Tasks) != 1 {
		t.Fatalf("tasks = %d", len(ledger.Tasks))
	}

	task := ledger.Tasks[0]
	if task.State != forum.StateDone {
		t.Fatalf("state = %s, want done", task.State)
	}

	if len(task.Events) != 2 || len(task.Ignored) != 0 {
		t.Fatalf("events = %d, ignored = %d", len(task.Events), len(task.Ignored))
	}

	acknowledged := append(append([]archive.ArchivedMessage{}, messages...),
		archived(t, replyEnvelope("01J9Z7QK3M4N5P6R7S8T9V0W4X", forum.KindAck, "ana@laptop", taskID), slackStamp(start.Add(6*time.Minute))))

	if got := forum.BuildLedger(ledgerArgs(acknowledged, now)).Tasks[0]; got.State != forum.StateAcknowledged {
		t.Fatalf("state = %s, want acknowledged", got.State)
	}
}

func TestLedgerRefusesMovesFromTheWrongSide(t *testing.T) {
	t.Parallel()

	const taskID = "01J9Z7QK3M4N5P6R7S8T9V0W1X"

	now := time.Date(2026, 11, 2, 10, 10, 0, 0, time.UTC)
	start := now.Add(-10 * time.Minute)

	messages := []archive.ArchivedMessage{
		archivedUser(t, taskEnvelope(taskID, "ana@laptop", "builder@laptop", "Sync it"), slackStamp(start), "U01ANA"),
		// The requester cannot accept their own task, and the addressee cannot ack it.
		archived(t, replyEnvelope("01J9Z7QK3M4N5P6R7S8T9V0W2X", forum.KindAccept, "ana@laptop", taskID), slackStamp(start.Add(2*time.Minute))),
		archived(t, replyEnvelope("01J9Z7QK3M4N5P6R7S8T9V0W3X", forum.KindAck, "builder@laptop", taskID), slackStamp(start.Add(4*time.Minute))),
	}

	task := forum.BuildLedger(ledgerArgs(messages, now)).Tasks[0]

	if task.State != forum.StateOpen {
		t.Fatalf("state = %s, want open", task.State)
	}

	if len(task.Ignored) != 2 {
		t.Fatalf("ignored = %d, want the two invalid moves kept", len(task.Ignored))
	}
}

func TestBroadcastIsClaimedNotDeclined(t *testing.T) {
	t.Parallel()

	const taskID = "01J9Z7QK3M4N5P6R7S8T9V0W1X"

	now := time.Date(2026, 11, 2, 10, 10, 0, 0, time.UTC)
	start := now.Add(-10 * time.Minute)

	messages := []archive.ArchivedMessage{
		archivedUser(t, taskEnvelope(taskID, "ana@laptop", "*", "Anyone?"), slackStamp(start), "U01ANA"),
		archived(t, replyEnvelope("01J9Z7QK3M4N5P6R7S8T9V0W2X", forum.KindDecline, "builder@laptop", taskID), slackStamp(start.Add(2*time.Minute))),
	}

	task := forum.BuildLedger(ledgerArgs(messages, now)).Tasks[0]

	if task.State != forum.StateOpen {
		t.Fatalf("one agent's decline closed the broadcast: %s", task.State)
	}

	messages = append(messages, archived(t, replyEnvelope("01J9Z7QK3M4N5P6R7S8T9V0W3X", forum.KindAccept, "builder@laptop", taskID), slackStamp(start.Add(4*time.Minute))))

	claimed := forum.BuildLedger(ledgerArgs(messages, now)).Tasks[0]

	if claimed.State != forum.StateAccepted || claimed.TakenBy != "builder@laptop" {
		t.Fatalf("the claim did not land: %s by %q", claimed.State, claimed.TakenBy)
	}
}

func TestLedgerExpiresAndStalls(t *testing.T) {
	t.Parallel()

	const taskID = "01J9Z7QK3M4N5P6R7S8T9V0W1X"

	sent := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	messages := []archive.ArchivedMessage{
		archivedUser(t, taskEnvelope(taskID, "ana@laptop", "builder@laptop", "Sync it"), slackStamp(sent), "U01ANA"),
	}

	// The ack window is 15 minutes: ten in, the task is open and not yet stalled.
	early := sent.Add(10 * time.Minute)

	if task := forum.BuildLedger(ledgerArgs(messages, early)).Tasks[0]; task.Stalled {
		t.Fatal("a task was stalled before its ack window passed")
	}

	// Twenty minutes in, nobody answered: stalled, and labelled with the silence.
	late := sent.Add(20 * time.Minute)

	task := forum.BuildLedger(ledgerArgs(messages, late)).Tasks[0]
	if !task.Stalled {
		t.Fatal("a task was not stalled after its ack window passed")
	}

	if label := forum.StateLabel(task, late); label != "open(stalled 20m)" {
		t.Fatalf("StateLabel = %q", label)
	}

	// A day and a bit later the ttl of 24 hours has passed: expired and closed.
	old := sent.Add(25 * time.Hour)

	expired := forum.BuildLedger(ledgerArgs(messages, old)).Tasks[0]
	if expired.State != forum.StateExpired || !expired.Closed {
		t.Fatalf("state = %s, closed = %v", expired.State, expired.Closed)
	}
}

func TestLedgerCountsWhatItCannotRead(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 2, 10, 10, 0, 0, time.UTC)

	broken := archive.ArchivedMessage{
		TS: "1793200000.000100", Chan: "C01FORUM",
		Files:    []archive.ArchivedFile{{ID: "F01", Name: "forum-01J9Z7QK3M4N5P6R7S8T9V0W1X.md"}},
		SyncedAt: "2026-11-02T10:00:00.000Z",
	}

	ledger := forum.BuildLedger(ledgerArgs([]archive.ArchivedMessage{broken}, now))

	if ledger.Unreadable != 1 {
		t.Fatalf("unreadable = %d, want 1: a message naming an envelope it cannot read is the defect", ledger.Unreadable)
	}

	orphan := archived(t, replyEnvelope("01J9Z7QK3M4N5P6R7S8T9V0W2X", forum.KindAccept, "builder@laptop", "01J9Z7QK3M4N5P6R7S8T9V0W9Z"), "1793200100.000100")

	if got := forum.BuildLedger(ledgerArgs([]archive.ArchivedMessage{orphan}, now)); got.Orphans != 1 {
		t.Fatalf("orphans = %d, want 1", got.Orphans)
	}
}

func archivedUser(t *testing.T, envelope *forum.Envelope, ts, user string) archive.ArchivedMessage {
	t.Helper()

	message := archived(t, envelope, ts)
	message.User = user

	return message
}
