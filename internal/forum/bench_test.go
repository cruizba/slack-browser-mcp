package forum_test

import (
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/forum"
)

// Every envelope a sync reads is decoded, and a channel with a thousand attachments is
// a normal channel, so this is on the path of a read rather than on the path of an
// edit. The sample is a task with a body: the heaviest shape the protocol has.
func benchmarkEnvelope() string {
	body := "the diff is in the branch; the tests cover the cursor rules and the deletions"

	envelope := forum.Envelope{
		V: 1, Kind: forum.KindTask, ID: "01J9Z7QK3M4N5P6R7S8T9V0W1X",
		From: "ana@laptop", To: "builder@laptop", Sent: "2026-11-02T09:00:00.000Z",
		Name: "ana", Host: "laptop", Title: "Review the sync", Body: body, HasBody: true,
		Capability: "review",
	}

	text, err := envelope.Encode()
	if err != nil {
		panic(err)
	}

	return text
}

func BenchmarkDecodeMarkdown(b *testing.B) {
	text := benchmarkEnvelope()

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		if forum.DecodeMarkdown(text) == nil {
			b.Fatal("the sample stopped decoding")
		}
	}
}

func BenchmarkEncodeMarkdown(b *testing.B) {
	text := benchmarkEnvelope()

	envelope := forum.DecodeMarkdown(text)
	if envelope == nil {
		b.Fatal("the sample does not decode")
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		if _, err := envelope.Encode(); err != nil {
			b.Fatalf("encode: %v", err)
		}
	}
}
