package slack_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/slack"
)

// An attachment is the one part of a post that reaches the channel as bytes the user
// chose, so the checks in front of it are the only thing between a typo and a 90 MB
// upload that Slack then refuses.

func writeFile(t *testing.T, name, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}

	return path
}

func TestHumanSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024*1024 - 1, "1024.0 KB"},
		{1024 * 1024, "1.0 MB"},
		{5 * 1024 * 1024, "5.0 MB"},
	}

	for _, test := range tests {
		if got := slack.HumanSize(test.bytes); got != test.want {
			t.Fatalf("HumanSize(%d) = %q, want %q", test.bytes, got, test.want)
		}
	}
}

func TestContentTypeOf(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"report.md":       "text/markdown",
		"SHOT.PNG":        "image/png",
		"data.json":       "application/json",
		"archive.tar.gz":  "application/gzip",
		"mystery":         "application/octet-stream",
		"mystery.binblob": "application/octet-stream",
	}

	for name, want := range tests {
		if got := slack.ContentTypeOf(name); got != want {
			t.Fatalf("ContentTypeOf(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestPlanAttachmentsDescribesWithoutReading(t *testing.T) {
	t.Parallel()

	path := writeFile(t, "report.md", "# the log")

	plans, err := slack.PlanAttachments([]string{path})
	if err != nil {
		t.Fatalf("PlanAttachments: %v", err)
	}

	if len(plans) != 1 {
		t.Fatalf("plans = %+v", plans)
	}

	// The name Slack shows is the basename: the directory a file came from is not the
	// channel's business, and it would leak the user's home layout.
	if plans[0].Name != "report.md" || plans[0].ContentType != "text/markdown" || plans[0].Bytes != int64(len("# the log")) {
		t.Fatalf("plan = %+v", plans[0])
	}

	if plans[0].Path != path {
		t.Fatalf("path = %q, want the one the caller wrote", plans[0].Path)
	}

	described := slack.DescribeAttachment(plans[0])
	if !strings.Contains(described, "report.md") || !strings.Contains(described, "text/markdown") ||
		!strings.Contains(described, "9 B") {
		t.Fatalf("DescribeAttachment = %q", described)
	}
}

func TestPlanAttachmentsRefusesWhatSlackWouldRefuse(t *testing.T) {
	t.Parallel()

	empty := writeFile(t, "empty.log", "")

	if _, err := slack.PlanAttachments([]string{empty}); err == nil ||
		!strings.Contains(err.Error(), "zero-byte") {
		t.Fatalf("an empty file was planned: %v", err)
	}

	if _, err := slack.PlanAttachments([]string{filepath.Join(t.TempDir(), "nope.md")}); err == nil ||
		!strings.Contains(err.Error(), "No file at") {
		t.Fatalf("a missing file was planned: %v", err)
	}

	// A directory is not a file, and saying so beats reading it and failing later.
	if _, err := slack.PlanAttachments([]string{t.TempDir()}); err == nil ||
		!strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("a directory was planned: %v", err)
	}

	tooMany := make([]string, slack.MaxAttachments+1)
	for index := range tooMany {
		tooMany[index] = writeFile(t, filepath.Base(filepath.Join(t.TempDir(), "f.md")), "x")
	}

	if _, err := slack.PlanAttachments(tooMany); err == nil ||
		!strings.Contains(err.Error(), "at most") {
		t.Fatalf("%d attachments were planned: %v", len(tooMany), err)
	}
}

func TestReadAttachmentsLoadsOnlyOnTheConfirmedPath(t *testing.T) {
	t.Parallel()

	path := writeFile(t, "log.txt", "hello")

	plans, err := slack.PlanAttachments([]string{path})
	if err != nil {
		t.Fatalf("PlanAttachments: %v", err)
	}

	uploads, err := slack.ReadAttachments(plans)
	if err != nil {
		t.Fatalf("ReadAttachments: %v", err)
	}

	if len(uploads) != 1 || uploads[0].Name != "log.txt" || string(uploads[0].Bytes) != "hello" {
		t.Fatalf("uploads = %+v", uploads)
	}

	// A plan whose file disappeared between the preview and the confirmation fails
	// with an error rather than sending an empty attachment.
	if err := os.Remove(path); err != nil {
		t.Fatalf("removing: %v", err)
	}

	if _, err := slack.ReadAttachments(plans); err == nil {
		t.Fatal("a vanished file was read")
	}
}
