package archive_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/archive"

	_ "modernc.org/sqlite"
)

// legacyIndexDDL is the statement the TypeScript build ran on every open, copied
// verbatim from the version that wrote it. It is here as a fixture, not as an
// aspiration: a user who upgraded keeps the index that build left behind, and this
// test is the only thing that notices if the shape drifts apart.
const legacyIndexDDL = `
  CREATE TABLE IF NOT EXISTS messages (
    team TEXT NOT NULL,
    ts TEXT NOT NULL,
    channel TEXT NOT NULL,
    channel_name TEXT,
    user TEXT,
    username TEXT,
    text TEXT,
    thread_ts TEXT,
    subtype TEXT,
    edited_ts TEXT,
    deleted INTEGER NOT NULL DEFAULT 0,
    reply_count INTEGER,
    PRIMARY KEY (team, ts)
  );
  CREATE INDEX IF NOT EXISTS messages_channel ON messages (team, channel, ts);
  CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    text, ts UNINDEXED, team UNINDEXED, channel UNINDEXED, tokenize='unicode61'
  );
`

// legacyIndex writes an index the way the deleted TypeScript build wrote one and
// returns its path.
func legacyIndex(t *testing.T) string {
	t.Helper()

	file := filepath.Join(t.TempDir(), "archive.sqlite")

	legacy, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatalf("opening the legacy index: %v", err)
	}

	if _, err := legacy.ExecContext(context.Background(), legacyIndexDDL); err != nil {
		t.Fatalf("writing the legacy schema: %v", err)
	}

	_, err = legacy.ExecContext(context.Background(),
		`INSERT INTO messages (team, ts, channel, channel_name, user, username, text, reply_count)
		 VALUES ('T000000001', '1793200000.000100', 'C01GENERAL', 'general', 'U01ANA', 'ana', 'the deploy is green', 3)`)
	if err != nil {
		t.Fatalf("writing a legacy row: %v", err)
	}

	if _, err := legacy.ExecContext(context.Background(),
		`INSERT INTO messages_fts (text, ts, team, channel) VALUES ('the deploy is green', '1793200000.000100', 'T000000001', 'C01GENERAL')`); err != nil {
		t.Fatalf("writing into the legacy fts table: %v", err)
	}

	_ = legacy.Close()

	return file
}

func TestAnIndexWrittenByTheOldBuildStaysReadable(t *testing.T) {
	t.Parallel()

	index := archive.NewIndex(legacyIndex(t))

	if err := index.Open(); err != nil {
		t.Fatalf("this build refuses an index the old one wrote: %v", err)
	}

	t.Cleanup(func() { _ = index.Close() })

	// A search has to work without a reindex, which is the whole promise of the
	// schema carrying over: the upgrade costs nothing and loses nothing.
	hits, err := index.Search(archive.SearchParams{Team: "T000000001", Query: "deploy", Limit: 10})
	if err != nil {
		t.Fatalf("searching an inherited index: %v", err)
	}

	if len(hits) != 1 || hits[0].Text != "the deploy is green" {
		t.Fatalf("hits = %+v", hits)
	}

	// The column the old build wrote and this one reads back has to survive whole:
	// a reply count of nil and a reply count of zero are different answers.
	if hits[0].ReplyCount == nil || *hits[0].ReplyCount != 3 {
		t.Fatalf("reply count = %v", hits[0].ReplyCount)
	}

	stats, err := index.Stats("T000000001")
	if err != nil {
		t.Fatalf("stats over an inherited index: %v", err)
	}

	if stats.Messages != 1 || stats.Newest != "1793200000.000100" {
		t.Fatalf("stats = %+v", stats)
	}

	// And a write on top of it works, so the two builds do not disagree about the
	// primary key either.
	second := "second"

	err = index.Upsert("T000000001", []archive.ArchivedMessage{{
		TS: "1793200001.000100", Chan: "C01GENERAL",
		ChannelName: "general", User: "U01ANA", Username: "ana", Text: &second,
	}})
	if err != nil {
		t.Fatalf("writing over an inherited index: %v", err)
	}

	if hits, err = index.Search(archive.SearchParams{Team: "T000000001", Query: "second"}); err != nil || len(hits) != 1 {
		t.Fatalf("after a write: hits=%+v err=%v", hits, err)
	}
}
