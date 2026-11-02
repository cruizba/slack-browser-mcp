package slack_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/slack"
)

// The audit log is the only record that a message was posted, since Slack cannot be
// asked afterwards, and it is the one file that holds user text: it keeps a redacted
// preview and never the message, which is the property these tests exist for.

func auditTime() time.Time {
	return time.Date(2026, 11, 2, 10, 30, 0, 0, time.UTC)
}

func readAudit(t *testing.T, file string) []map[string]any {
	t.Helper()

	content, err := os.ReadFile(file) //nolint:gosec // the path is this test's own temporary directory
	if err != nil {
		t.Fatalf("reading the audit log: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	entries := make([]map[string]any, 0, len(lines))

	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("line %q is not JSON: %v", line, err)
		}

		entries = append(entries, entry)
	}

	return entries
}

func TestRecordSendKeepsAPreviewAndNeverTheMessage(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "audit.jsonl")
	long := strings.Repeat("a", 500)

	written, err := slack.RecordSend(file, slack.SendRecord{
		Workspace: "CodeURJC", Channel: "#general", ChannelID: "C01GENERAL",
		MessageTS: "1793200000.000100", Text: long, Files: []string{"report.md (9 B)"},
	}, auditTime())
	if err != nil {
		t.Fatalf("RecordSend: %v", err)
	}

	if written != file {
		t.Fatalf("RecordSend answered %q, want the file it appended to", written)
	}

	entries := readAudit(t, file)
	if len(entries) != 1 {
		t.Fatalf("entries = %d", len(entries))
	}

	entry := entries[0]

	// The count is what a reader uses to know how much was said; the preview is
	// capped, so a 500-character message does not become a 500-character log line.
	chars, isNumber := entry["chars"].(float64)
	if !isNumber || chars != 500 {
		t.Fatalf("chars = %v, want 500", entry["chars"])
	}

	preview, isText := entry["preview"].(string)
	if !isText || len([]rune(preview)) != 120 {
		t.Fatalf("preview is %v, want 120 runes", entry["preview"])
	}

	if entry["at"] != "2026-11-02T10:30:00.000Z" {
		t.Fatalf("at = %v", entry["at"])
	}

	// A send has no action: the absence is what tells it apart from a removal.
	if _, present := entry["action"]; present {
		t.Fatalf("a send carried an action: %v", entry["action"])
	}

	if files, ok := entry["files"].([]any); !ok || len(files) != 1 {
		t.Fatalf("files = %v", entry["files"])
	}
}

func TestAuditRedactsWhatLooksLikeASecret(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "audit.jsonl")

	// A user pasting a token into Slack is the exact case the audit must not keep.
	_, err := slack.RecordSend(file, slack.SendRecord{
		Workspace: "CodeURJC", Channel: "#general", ChannelID: "C01GENERAL",
		Text: "my token is xoxc-1234567890-abcdef and the cookie xoxd-abcdef",
	}, auditTime())
	if err != nil {
		t.Fatalf("RecordSend: %v", err)
	}

	entries := readAudit(t, file)

	preview, isText := entries[0]["preview"].(string)
	if !isText {
		t.Fatalf("preview = %v", entries[0]["preview"])
	}

	if strings.Contains(preview, "xoxc-1234567890-abcdef") || strings.Contains(preview, "xoxd-abcdef") {
		t.Fatalf("the audit kept a token: %q", preview)
	}

	if !strings.Contains(preview, "my token is") {
		t.Fatalf("the audit lost the sentence around the secret: %q", preview)
	}
}

func TestRecordRemovalNamesTheAction(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "audit.jsonl")

	for _, action := range []string{"delete", "edit"} {
		if _, err := slack.RecordRemoval(file, slack.RemovalRecord{
			Action: action, Workspace: "CodeURJC", Channel: "#general",
			ChannelID: "C01GENERAL", MessageTS: "1793200000.000100", Text: "hello",
		}, auditTime()); err != nil {
			t.Fatalf("RecordRemoval(%s): %v", action, err)
		}
	}

	entries := readAudit(t, file)
	if len(entries) != 2 || entries[0]["action"] != "delete" || entries[1]["action"] != "edit" {
		t.Fatalf("entries = %+v", entries)
	}

	// A removal keeps the thread it belonged to, because the message is gone from
	// Slack and this is all that is left.
	if entries[0]["message_ts"] != "1793200000.000100" {
		t.Fatalf("message_ts = %v", entries[0]["message_ts"])
	}
}

func TestAuditAppendsAndIsPrivate(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "nested", "audit.jsonl")

	for range 2 {
		if _, err := slack.RecordSend(file, slack.SendRecord{
			Workspace: "CodeURJC", Channel: "#general", ChannelID: "C01GENERAL", Text: "hi",
		}, auditTime()); err != nil {
			t.Fatalf("RecordSend: %v", err)
		}
	}

	// Two sends are two lines: a log that overwrote itself would be worse than none.
	if entries := readAudit(t, file); len(entries) != 2 {
		t.Fatalf("entries = %d, want the log to append", len(entries))
	}

	info, err := os.Stat(file)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Windows has no mode bits; everywhere else the log holds user text and only the
	// user may read it.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %#o, want 0600", info.Mode().Perm())
	}
}
