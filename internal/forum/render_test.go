package forum_test

import (
	"strings"
	"testing"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/forum"
)

func renderContext(t *testing.T) forum.Context {
	t.Helper()

	context, _ := serviceFixture(t)

	return context
}

func TestAgo(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		ts   string
		want string
	}{
		{name: "under a minute", ts: slackStamp(now.Add(-30 * time.Second)), want: "now"},
		{name: "minutes", ts: slackStamp(now.Add(-5 * time.Minute)), want: "5m"},
		{name: "hours", ts: slackStamp(now.Add(-3 * time.Hour)), want: "3h"},
		{name: "days", ts: slackStamp(now.Add(-72 * time.Hour)), want: "3d"},
		{name: "nothing to measure from", ts: "not-a-ts", want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := forum.Ago(tt.ts, now); got != tt.want {
				t.Fatalf("Ago = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDecisionLabelSeparatesTheChannelFromTheLocalGate(t *testing.T) {
	t.Parallel()

	if got := forum.DecisionLabel(forum.Decision{}, false); got != "local:-" {
		t.Fatalf("no decision = %q", got)
	}

	decision := forum.Decision{Decision: "approved"}

	if got := forum.DecisionLabel(decision, true); got != "local:approved" {
		t.Fatalf("a human approval = %q", got)
	}

	// An agent deciding for itself is visible on purpose: it prevents nothing, but it
	// makes the pattern readable in the inbox and in the audit.
	decision.SelfApproved = true

	if got := forum.DecisionLabel(decision, true); got != "local:approved(self)" {
		t.Fatalf("a self approval = %q", got)
	}
}

func TestChannelLabelIsWhatAPersonTyped(t *testing.T) {
	t.Parallel()

	if got := forum.ChannelLabel(forum.Binding{Name: "general"}); got != "#general" {
		t.Fatalf("public = %q", got)
	}

	if got := forum.ChannelLabel(forum.Binding{Name: "leads", Private: true}); got != "private:leads" {
		t.Fatalf("private = %q", got)
	}
}

func TestInboxLineCarriesWhatTheReaderNeeds(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

	task := forum.Task{
		ID: "01J9Z7QK3M4N5P6R7S8T9V0W1X", Title: "Review the sync",
		State: forum.StateOpen, From: "ana@laptop", TS: slackStamp(now.Add(-5 * time.Minute)),
		Capability: "review", TakenBy: "builder@laptop",
	}

	line := forum.InboxLine(task, forum.Decision{}, false, now, "ci-notifications")

	for _, wanted := range []string{
		"01J9Z7QK3M4N5P6R7S8T9V0W1X", "open", "local:-",
		"<- ana@laptop", "taken:builder@laptop", "[review]", "Review the sync", "5m",
	} {
		if !strings.Contains(line, wanted) {
			t.Fatalf("the line does not carry %q: %s", wanted, line)
		}
	}
}

func TestRenderPeers(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

	view := forum.BindingView{
		Binding: forum.Binding{Name: "forum", Channel: "C01FORUM"},
		Roster: []forum.Peer{
			{
				From: "builder@laptop", Name: "builder", Host: "laptop",
				Capabilities: []string{"review"}, TS: slackStamp(now.Add(-time.Minute)),
				InteractiveTS: slackStamp(now.Add(-10 * time.Second)),
			},
			{
				From: "gone@desk", Name: "gone", Host: "desk",
				TS: slackStamp(now.Add(-96 * time.Hour)),
			},
		},
	}

	lines := strings.Join(forum.RenderPeers(view, now, forum.DefaultLimits), "\n")

	if !strings.Contains(lines, "#forum (C01FORUM)") {
		t.Fatalf("the channel header is missing:\n%s", lines)
	}

	if !strings.Contains(lines, "builder@laptop") || !strings.Contains(lines, "· can review") {
		t.Fatalf("a peer is missing its capabilities:\n%s", lines)
	}

	if !strings.Contains(lines, "up · here, active now") {
		t.Fatalf("a live peer is not shown as here:\n%s", lines)
	}

	// Silence is the only way out, and it reads as such.
	if !strings.Contains(lines, "gone@desk") || !strings.Contains(lines, "out, last seen 4d ago") {
		t.Fatalf("a silent peer was not aged out:\n%s", lines)
	}

	empty := forum.RenderPeers(forum.BindingView{Binding: forum.Binding{Name: "forum"}}, now, forum.DefaultLimits)
	if !strings.Contains(strings.Join(empty, "\n"), "nobody registered yet") {
		t.Fatalf("an empty roster = %v", empty)
	}
}

func TestRenderTask(t *testing.T) {
	t.Parallel()

	context := renderContext(t)
	now := context.Now

	task := forum.Task{
		ID: "01J9Z7QK3M4N5P6R7S8T9V0W1X", Title: "Review the sync", Body: "the diff",
		State: forum.StateAccepted, From: "ana@laptop", To: "builder@laptop",
		Sent: "2026-11-02T09:00:00.000Z", Capability: "review", Reopens: 1,
		TS: slackStamp(now.Add(-time.Hour)), LastActivityTS: slackStamp(now.Add(-time.Minute)),
		Events: []forum.TaskEvent{
			{Kind: forum.KindAccept, From: "builder@laptop", TS: "1793200100.000100"},
		},
		Ignored: []forum.TaskEvent{
			{Kind: forum.KindAck, From: "builder@laptop"},
		},
	}

	text := forum.RenderTask(context, task, forum.Binding{Name: "forum", Channel: "C01FORUM", Workspace: "T01ACME"},
		forum.Decision{}, false, nil)

	for _, wanted := range []string{
		"01J9Z7QK3M4N5P6R7S8T9V0W1X", "accepted", "local:-", "T01ACME/#forum",
		"ana@laptop -> builder@laptop", "needs review", "reopened 1x",
		"Review the sync", "the diff", "[1793200100.000100] accept from builder@laptop",
		"1 ignored event(s): ack from builder@laptop",
	} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("the task does not carry %q:\n%s", wanted, text)
		}
	}
}

func TestRenderTaskTruncatesABodyThatWouldBuryTheThread(t *testing.T) {
	t.Parallel()

	context := renderContext(t)

	task := forum.Task{
		ID: "01J9Z7QK3M4N5P6R7S8T9V0W1X", Title: "Huge", State: forum.StateOpen,
		From: "ana@laptop", To: "builder@laptop", Sent: "2026-11-02T09:00:00.000Z",
		Body: strings.Repeat("a", 5000), FileID: "F01ENVELOPE",
		TS: "1793200000.000100",
	}

	text := forum.RenderTask(context, task, forum.Binding{Name: "forum"}, forum.Decision{}, false, nil)

	if !strings.Contains(text, "truncated: 5000 characters") {
		t.Fatalf("a huge body was not marked:\n%s", text[len(text)-120:])
	}

	// The cut says where the rest lives, which is the archive copy of the envelope.
	if !strings.Contains(text, "read_file F01ENVELOPE") {
		t.Fatalf("the cut does not say where the rest is:\n%s", text[len(text)-160:])
	}
}

func TestRenderInbox(t *testing.T) {
	t.Parallel()

	context := renderContext(t)

	report := forum.InboxReport{
		Views: []forum.BindingView{{Binding: forum.Binding{Name: "forum", Channel: "C01FORUM"}}},
		Entries: []forum.InboxEntry{{
			Task: forum.Task{
				ID: "01J9Z7QK3M4N5P6R7S8T9V0W1X", Title: "Review", State: forum.StateOpen,
				From: "ana@laptop", TS: "1793200000.000100",
			},
		}},
		Taken: 1, Hidden: 2, Unreadable: 3, Orphans: 4,
	}

	text := forum.RenderInbox(context, report, true, " · a note")

	if !strings.Contains(text, "agent forum as ana@laptop") {
		t.Fatalf("inbox = %q", text)
	}

	if !strings.Contains(text, "1 task(s) · 1 taken · 2 from unknown or retired peers") {
		t.Fatalf("the counts are missing: %q", text)
	}

	if !strings.Contains(text, "3 unreadable envelope(s) · 4 orphan repl(y/ies)") {
		t.Fatalf("the defects are missing: %q", text)
	}

	if !strings.Contains(text, "· offline") || !strings.Contains(text, " · a note") {
		t.Fatalf("the state of the read is missing: %q", text)
	}

	empty := forum.RenderInbox(context, forum.InboxReport{}, false, "")
	if !strings.Contains(empty, "nothing pending") || !strings.Contains(empty, "no channel bound") {
		t.Fatalf("an empty inbox = %q", empty)
	}
}
