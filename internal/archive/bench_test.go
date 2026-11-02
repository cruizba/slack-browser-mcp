package archive_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/archive"
)

// The archive is the only place where the cost of a read is visible to an agent: a
// search that walks the JSONL instead of the index, or an append that rewrites the
// whole file, feels like a slow answer rather than a slow test. These are the numbers
// to compare against after touching either path.
const benchMessages = 2000

func benchRecords() []archive.ArchivedMessage {
	base := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	records := make([]archive.ArchivedMessage, 0, benchMessages)

	for index := range benchMessages {
		text := fmt.Sprintf("message %d about the deploy pipeline and its retries", index)
		records = append(records, archive.ArchivedMessage{
			TS:          fmt.Sprintf("%d.%06d", base.Add(time.Duration(index)*time.Minute).Unix(), index),
			Chan:        "C01GENERAL",
			ChannelName: "general",
			User:        "U01ANA",
			Username:    "ana",
			Text:        &text,
		})
	}

	return records
}

func benchStore(b *testing.B) (*archive.Store, *archive.Index) {
	b.Helper()

	root := filepath.Join(b.TempDir(), "archive")

	store := archive.NewStore(root)
	if err := store.Append("T000000001", "C01GENERAL", benchRecords()); err != nil {
		b.Fatalf("seeding the store: %v", err)
	}

	index := archive.NewIndex(filepath.Join(root, "archive.sqlite"))
	if err := index.Upsert("T000000001", benchRecords()); err != nil {
		b.Fatalf("seeding the index: %v", err)
	}

	b.Cleanup(func() { _ = index.Close() })

	return store, index
}

func BenchmarkReadChannel(b *testing.B) {
	store, _ := benchStore(b)

	b.ResetTimer()

	for b.Loop() {
		if _, err := store.Read("T000000001", "C01GENERAL"); err != nil {
			b.Fatalf("read: %v", err)
		}
	}
}

func BenchmarkAppendOne(b *testing.B) {
	store, _ := benchStore(b)
	text := "a new message"

	b.ResetTimer()

	for index := range b.N {
		record := []archive.ArchivedMessage{{
			TS:   fmt.Sprintf("%d.%06d", 1_800_000_000+index, index),
			Chan: "C01GENERAL", ChannelName: "general", Text: &text,
		}}

		if err := store.Append("T000000001", "C01GENERAL", record); err != nil {
			b.Fatalf("append: %v", err)
		}
	}
}

func BenchmarkSearch(b *testing.B) {
	_, index := benchStore(b)

	b.ResetTimer()

	for b.Loop() {
		hits, err := index.Search(archive.SearchParams{Team: "T000000001", Query: "deploy retries", Limit: 20})
		if err != nil {
			b.Fatalf("search: %v", err)
		}

		if len(hits) == 0 {
			b.Fatal("the seeded messages were not found")
		}
	}
}

func BenchmarkList(b *testing.B) {
	_, index := benchStore(b)

	b.ResetTimer()

	for b.Loop() {
		if _, err := index.List(archive.ListParams{Team: "T000000001", Newest: true, Limit: 50}); err != nil {
			b.Fatalf("list: %v", err)
		}
	}
}
