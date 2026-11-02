package forum_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/forum"
)

// The decision log is the local record of consent: which task a human approved, and
// whether an agent decided for itself. It prevents nothing — a local file cannot — so
// what it has to be is honest and durable: append-only, last line wins, and a
// self-approval visible as one.

func decisionTime() time.Time {
	return time.Date(2026, 11, 2, 10, 30, 0, 0, time.UTC)
}

func TestRecordAndReadTheLastDecision(t *testing.T) {
	t.Parallel()

	log := forum.NewDecisionLog(t.TempDir())

	first, err := log.Record("T000000001", "C01FORUM", forum.DecisionInput{
		Task: "01J9Z7QK3M4N5P6R7S8T9V0W1X", Decision: "accepted", By: "ana",
	}, decisionTime())
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	// The second decision for the same task is what wins: an approval that was later
	// withdrawn is a withdrawal, not two facts of equal weight.
	if _, err := log.Record("T000000001", "C01FORUM", forum.DecisionInput{
		Task: "01J9Z7QK3M4N5P6R7S8T9V0W1X", Decision: "rejected", By: "ana",
	}, decisionTime()); err != nil {
		t.Fatalf("Record: %v", err)
	}

	decisions, err := log.Read("T000000001", "C01FORUM")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	decision, known := decisions["01J9Z7QK3M4N5P6R7S8T9V0W1X"]
	if !known || decision.Decision != "rejected" {
		t.Fatalf("decisions = %+v", decisions)
	}

	if decision.At != "2026-11-02T10:30:00.000Z" {
		t.Fatalf("at = %q", decision.At)
	}

	// The file is append-only: both decisions are still there, which is what makes it a
	// record rather than a state file.
	content, err := os.ReadFile(first) //nolint:gosec // the path is this test's own temporary directory
	if err != nil {
		t.Fatalf("reading: %v", err)
	}

	if lines := strings.Count(strings.TrimSpace(string(content)), "\n") + 1; lines != 2 {
		t.Fatalf("the log holds %d lines, want 2", lines)
	}
}

func TestSelfApprovedIsVisible(t *testing.T) {
	t.Parallel()

	log := forum.NewDecisionLog(t.TempDir())

	if _, err := log.Record("T000000001", "C01FORUM", forum.DecisionInput{
		Task: "01J9Z7QK3M4N5P6R7S8T9V0W1X", Decision: "accepted", By: "agent",
	}, decisionTime()); err != nil {
		t.Fatalf("Record: %v", err)
	}

	decisions, err := log.Read("T000000001", "C01FORUM")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if !decisions["01J9Z7QK3M4N5P6R7S8T9V0W1X"].SelfApproved {
		t.Fatal("an agent's own approval is not marked")
	}

	// A human's is not marked, because the field means "nobody else was asked".
	if got := forum.DecisionLabel(decisions["01J9Z7QK3M4N5P6R7S8T9V0W1X"], true); got != "local:accepted(self)" {
		t.Fatalf("label = %q", got)
	}

	if _, err := log.Record("T000000001", "C01FORUM", forum.DecisionInput{
		Task: "01J9Z7QK3M4N5P6R7S8T9V0W2X", Decision: "accepted", By: "ana",
	}, decisionTime()); err != nil {
		t.Fatalf("Record: %v", err)
	}

	decisions, err = log.Read("T000000001", "C01FORUM")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if decisions["01J9Z7QK3M4N5P6R7S8T9V0W2X"].SelfApproved {
		t.Fatal("a human's approval was marked as self-approved")
	}
}

func TestDecisionLogToleratesWhatItCannotUse(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	log := forum.NewDecisionLog(root)

	// Nothing recorded yet is an empty map, not an error: every read of a fresh machine
	// would otherwise fail.
	decisions, err := log.Read("T000000001", "C01FORUM")
	if err != nil || len(decisions) != 0 {
		t.Fatalf("Read on a fresh log = %+v, %v", decisions, err)
	}

	file, err := log.File("T000000001", "C01FORUM")
	if err != nil {
		t.Fatalf("File: %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatalf("creating the directory: %v", err)
	}

	// A half-written last line is what a crash leaves. Losing that line is acceptable;
	// failing every read from then on is not.
	content := `{"at":"2026-11-02T10:00:00.000Z","task":"one","decision":"accepted","by":"ana"}
not json at all
`
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	decisions, err = log.Read("T000000001", "C01FORUM")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(decisions) != 1 || decisions["one"].Decision != "accepted" {
		t.Fatalf("decisions = %+v, want the readable line kept", decisions)
	}

	// An id that would escape the directory is refused before any file is opened.
	if _, err := log.File("../escape", "C01FORUM"); err == nil {
		t.Fatal("a traversing team id was accepted")
	}

	if _, err := log.File("T000000001", "../escape"); err == nil {
		t.Fatal("a traversing channel id was accepted")
	}
}
