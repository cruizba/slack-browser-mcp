package forum_test

import (
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/forum"
)

// inboxFixture is a bound channel whose ledger holds the tasks a filter has to judge.
func inboxFixture(t *testing.T, tasks []forum.Task, decisions map[string]forum.Decision) (forum.Context, forum.BindingView) {
	t.Helper()

	context, _ := serviceFixture(t)

	view := forum.BindingView{
		Binding:   context.File.Channels[0],
		Key:       context.File.Channels[0].Key(),
		Ledger:    forum.Ledger{Tasks: tasks},
		Decisions: decisions,
	}

	return context, view
}

func taskFor(id, from, to string, state forum.TaskState, ts string) forum.Task {
	// The ledger marks a task closed when it reaches a state nobody waits on; a fixture
	// that skipped that would test a task no derivation can produce.
	closed := state == forum.StateAcknowledged || state == forum.StateCancelled ||
		state == forum.StateDeclined || state == forum.StateExpired || state == forum.StateDeleted

	return forum.Task{ID: id, Title: id, From: from, To: to, State: state, TS: ts, Closed: closed}
}

func TestInboxKeepsOnlyWhatIsSomebodyElsesToPickUp(t *testing.T) {
	t.Parallel()

	me := "ana@laptop"

	tasks := []forum.Task{
		taskFor("01J9Z7QK3M4N5P6R7S8T9V0W0A", "builder@laptop", me, forum.StateOpen, "1793200100.000100"),
		taskFor("01J9Z7QK3M4N5P6R7S8T9V0W0B", "builder@laptop", "other@desk", forum.StateOpen, "1793200100.000200"),
		taskFor("01J9Z7QK3M4N5P6R7S8T9V0W0C", me, "builder@laptop", forum.StateOpen, "1793200100.000300"),
		taskFor("01J9Z7QK3M4N5P6R7S8T9V0W0D", "builder@laptop", "*", forum.StateAcknowledged, "1793200100.000400"),
		taskFor("01J9Z7QK3M4N5P6R7S8T9V0W0E", "builder@laptop", me, forum.StateOpen, "1793200100.000500"),
	}

	context, view := inboxFixture(t, tasks, map[string]forum.Decision{
		"01J9Z7QK3M4N5P6R7S8T9V0W0E": {Task: "01J9Z7QK3M4N5P6R7S8T9V0W0E", Decision: "rejected"},
	})

	report := forum.InboxEntries(context, []forum.BindingView{view}, forum.InboxOptions{})

	ids := make([]string, 0, len(report.Entries))
	for _, entry := range report.Entries {
		ids = append(ids, entry.Task.ID)
	}

	// Only the first: not mine by address is not mine to do, my own task is not work I
	// pick up, a closed one is done, and a rejected one was decided already.
	if len(ids) != 1 || ids[0] != "01J9Z7QK3M4N5P6R7S8T9V0W0A" {
		t.Fatalf("entries = %v", ids)
	}
}

func TestInboxShowsATakenBroadcastAndCountsIt(t *testing.T) {
	t.Parallel()

	taken := taskFor("01J9Z7QK3M4N5P6R7S8T9V0W1A", "builder@laptop", "*", forum.StateOpen, "1793200100.000100")
	taken.TakenBy = "reviewer@desk"

	claimed := taskFor("01J9Z7QK3M4N5P6R7S8T9V0W1B", "builder@laptop", "*", forum.StateOpen, "1793200100.000200")
	claimed.TakenBy = "ana@laptop"

	context, view := inboxFixture(t, []forum.Task{taken, claimed}, nil)

	report := forum.InboxEntries(context, []forum.BindingView{view}, forum.InboxOptions{})

	// Hiding work somebody else has is what makes a second agent think nobody took it.
	if len(report.Entries) != 2 || report.Taken != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestInboxAllShowsEverything(t *testing.T) {
	t.Parallel()

	tasks := []forum.Task{
		taskFor("01J9Z7QK3M4N5P6R7S8T9V0W2A", "builder@laptop", "other@desk", forum.StateOpen, "1793200100.000100"),
		taskFor("01J9Z7QK3M4N5P6R7S8T9V0W2B", "builder@laptop", "ana@laptop", forum.StateDone, "1793200100.000200"),
	}

	context, view := inboxFixture(t, tasks, nil)

	report := forum.InboxEntries(context, []forum.BindingView{view}, forum.InboxOptions{All: true})

	if len(report.Entries) != 2 {
		t.Fatalf("entries = %d", len(report.Entries))
	}

	// Ordered oldest first, so a reader follows the conversation.
	if report.Entries[0].Task.TS > report.Entries[1].Task.TS {
		t.Fatalf("the report is not ordered: %v", report.Entries)
	}
}

func TestInboxRespectsTheBoundaryOfTheBinding(t *testing.T) {
	t.Parallel()

	old := taskFor("01J9Z7QK3M4N5P6R7S8T9V0W3A", "builder@laptop", "ana@laptop", forum.StateOpen, "1793200000.000100")
	recent := taskFor("01J9Z7QK3M4N5P6R7S8T9V0W3B", "builder@laptop", "ana@laptop", forum.StateOpen, "1793200200.000100")

	context, view := inboxFixture(t, []forum.Task{old, recent}, nil)
	view.Binding.SinceTS = "1793200100.000000"

	report := forum.InboxEntries(context, []forum.BindingView{view}, forum.InboxOptions{})

	if len(report.Entries) != 1 || report.Entries[0].Task.ID != "01J9Z7QK3M4N5P6R7S8T9V0W3B" {
		t.Fatalf("entries = %+v", report.Entries)
	}

	// Ignoring the boundary is what shows the history the binding can see.
	all := forum.InboxEntries(context, []forum.BindingView{view}, forum.InboxOptions{SinceStart: true})
	if len(all.Entries) != 2 {
		t.Fatalf("since start = %d entries", len(all.Entries))
	}
}

func TestWaitForWorkAnswersImmediatelyWhenSomethingIsPending(t *testing.T) {
	t.Parallel()

	context, store := serviceFixture(t)
	seedPeer(t, store, "builder", "laptop")
	seedTask(t, store, "builder@laptop", "ana@laptop", "Review it")

	// The loop reads the archive itself, so the task has to be there; what matters is
	// that it stops on the first cycle instead of sleeping out its budget.
	result := forum.WaitForWork(context, forum.WaitOptions{Seconds: 5, Offline: true})

	if result.Cycles != 1 {
		t.Fatalf("cycles = %d, want one look", result.Cycles)
	}

	if len(result.Report.Entries) != 1 {
		t.Fatalf("entries = %d", len(result.Report.Entries))
	}
}

func TestRenderInboxNamesThePeerBehindALabel(t *testing.T) {
	t.Parallel()

	view := forum.BindingView{
		Binding: forum.Binding{Name: "forum", Channel: "C01FORUM"},
		Roster:  []forum.Peer{{From: "builder@laptop", Name: "builder", Host: "laptop", Owner: "U01ANA"}},
		People:  map[string]string{"U01ANA": "Ana Ruiz"},
	}

	if got := forum.PeerLabel("builder@laptop", &view); got != "builder@laptop (Ana Ruiz)" {
		t.Fatalf("PeerLabel = %q", got)
	}

	if got := forum.PeerLabel("stranger@desk", &view); got != "stranger@desk" {
		t.Fatalf("PeerLabel of a stranger = %q", got)
	}
}
