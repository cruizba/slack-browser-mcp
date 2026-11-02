package envelope_test

import (
	"strings"
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/envelope"
)

func TestFileNameRoundTrips(t *testing.T) {
	t.Parallel()

	name := envelope.FileName("01J9Z7QK3M4N5P6R7S8T9V0W1X")

	if name != "forum-01J9Z7QK3M4N5P6R7S8T9V0W1X.md" {
		t.Fatalf("FileName = %q", name)
	}

	if !envelope.IsFileName(name) {
		t.Fatalf("IsFileName(%q) = false", name)
	}

	// The legacy shape is still an envelope, which is why older messages still read.
	if !envelope.IsFileName("forum-01J9Z7QK3M4N5P6R7S8T9V0W1X.json") {
		t.Fatal("a legacy envelope file was not recognised")
	}
}

func TestIsFileNameRefusesWhatIsNotAnEnvelope(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"",
		"forum-",
		"forum-.md",
		"forum-.json",
		"report.pdf",
		"forum-report.txt",
		"my-forum-report.md",
	} {
		if envelope.IsFileName(name) {
			t.Fatalf("IsFileName(%q) = true", name)
		}
	}
}

func TestFileOfPicksTheEnvelopeAmongAttachments(t *testing.T) {
	t.Parallel()

	files := []envelope.CarriedFile{
		{Name: "screenshot.png", Size: 1000},
		{Name: "forum-01J9Z7QK3M4N5P6R7S8T9V0W1X.md", Size: 400},
	}

	chosen, ok := envelope.FileOf(files)
	if !ok || !strings.HasSuffix(chosen.Name, ".md") {
		t.Fatalf("FileOf = %+v, %v", chosen, ok)
	}

	if _, ok := envelope.FileOf([]envelope.CarriedFile{{Name: "screenshot.png"}}); ok {
		t.Fatal("a message with no envelope answered one")
	}
}

func TestFileOfRefusesAnOversizedForumFile(t *testing.T) {
	t.Parallel()

	// A `forum-*` bigger than the body cap is something a person attached, and the sync
	// must not drag it into the archive.
	oversized := []envelope.CarriedFile{
		{Name: envelope.FileName("01J9Z7QK3M4N5P6R7S8T9V0W1X"), Size: envelope.MaxFileBytes + 1},
	}

	if _, ok := envelope.FileOf(oversized); ok {
		t.Fatal("an oversized forum file was treated as an envelope")
	}
}
