package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	_ "modernc.org/sqlite" // CGO-free SQLite, so the cross-compiled release builds without a C toolchain

	"github.com/cruizba/slack-browser-mcp/internal/platform"
	"github.com/cruizba/slack-browser-mcp/internal/redact"
)

const (
	searchPrefix = `
      SELECT m.team, m.channel, m.channel_name, m.ts, m.user, m.username, m.text,
             m.thread_ts, m.deleted, m.reply_count
      FROM messages_fts
      JOIN messages m ON m.team = messages_fts.team AND m.ts = messages_fts.ts
      WHERE `

	searchSuffix = `
      ORDER BY bm25(messages_fts)
      LIMIT ?`

	listPrefix = `
      SELECT team, channel, channel_name, ts, user, username, text, thread_ts,
             deleted, reply_count
      FROM messages
      `

	listMiddle = `
      ORDER BY CAST(ts AS REAL) `

	listSuffix = `
      LIMIT ?`
)

const schemaSQL = `
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

// SearchHit is one row of a search or a timeline.
type SearchHit struct {
	Team        string
	Channel     string
	ChannelName string
	TS          string
	User        string
	Username    string
	Text        string
	ThreadTS    string
	Deleted     bool
	ReplyCount  *int
}

// SearchParams is what a full-text search is asked for.
type SearchParams struct {
	Query          string
	Team           string
	Channel        string
	ChannelName    string
	Limit          int
	IncludeDeleted bool
}

// ListParams is what a timeline is asked for. It answers what full text cannot
// ("the oldest messages", "the first one in #x"): no words to match, just an
// order.
type ListParams struct {
	Team           string
	Channel        string
	ChannelName    string
	Authors        []string
	Oldest         string
	Latest         string
	Newest         bool
	Limit          int
	IncludeDeleted bool
	IncludeReplies bool
}

// Stats is what the index knows about, without reading the JSONL.
type Stats struct {
	Messages int
	Deleted  int
	Channels int
	Oldest   string
	Newest   string
}

// Index is the SQLite index over the JSONL archive. Derived data only: `archive
// reindex` rebuilds it from scratch, so a damaged index costs time, never history.
type Index struct {
	file string
	db   *sql.DB
}

// NewIndex points at the index file.
func NewIndex(file string) *Index {
	return &Index{file: file}
}

// Open connects, creating the schema. WAL lets a read tool write back what it
// fetched while a sync process indexes.
func (i *Index) Open() error {
	if i.db != nil {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(i.file), 0o700); err != nil {
		return err
	}

	db, err := sql.Open("sqlite", i.file)
	if err != nil {
		return err
	}

	// A single connection keeps the PRAGMAs in force: they belong to the
	// connection, not the file.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.ExecContext(context.Background(), pragma); err != nil {
			_ = db.Close()

			return err
		}
	}

	if _, err := db.ExecContext(context.Background(), schemaSQL); err != nil {
		_ = db.Close()

		return err
	}

	i.db = db

	return platform.Harden(i.file)
}

// Close releases the connection.
func (i *Index) Close() error {
	if i.db == nil {
		return nil
	}

	err := i.db.Close()
	i.db = nil

	return err
}

// Upsert writes records into the index, replacing what was there. A tombstone
// carries no text, so the row keeps the words it had: search is the only way to
// find a message that Slack no longer serves.
func (i *Index) Upsert(team string, records []ArchivedMessage) error {
	if len(records) == 0 {
		return nil
	}

	if err := i.Open(); err != nil {
		return err
	}

	tx, err := i.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback() }()

	statements, err := prepareIndexStatements(tx)
	if err != nil {
		return err
	}

	if err := statements.write(team, records); err != nil {
		return err
	}

	return tx.Commit()
}

// indexStatements are the four prepared statements one batch of records needs.
type indexStatements struct {
	upsert *sql.Stmt
	drop   *sql.Stmt
	add    *sql.Stmt
	stored *sql.Stmt
}

func prepareIndexStatements(tx *sql.Tx) (*indexStatements, error) {
	ctx := context.Background()

	upsert, err := tx.PrepareContext(ctx, `
      INSERT INTO messages (team, ts, channel, channel_name, user, username, text, thread_ts, subtype, edited_ts, deleted, reply_count)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT (team, ts) DO UPDATE SET
        channel = excluded.channel,
        channel_name = coalesce(excluded.channel_name, messages.channel_name),
        user = coalesce(excluded.user, messages.user),
        username = coalesce(excluded.username, messages.username),
        text = coalesce(excluded.text, messages.text),
        thread_ts = coalesce(excluded.thread_ts, messages.thread_ts),
        subtype = coalesce(excluded.subtype, messages.subtype),
        edited_ts = coalesce(excluded.edited_ts, messages.edited_ts),
        deleted = excluded.deleted,
        reply_count = coalesce(excluded.reply_count, messages.reply_count)
    `)
	if err != nil {
		return nil, err
	}

	drop, err := tx.PrepareContext(ctx, `DELETE FROM messages_fts WHERE team = ? AND ts = ?`)
	if err != nil {
		return nil, err
	}

	add, err := tx.PrepareContext(ctx, `INSERT INTO messages_fts (text, ts, team, channel) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return nil, err
	}

	stored, err := tx.PrepareContext(ctx, `SELECT text FROM messages WHERE team = ? AND ts = ?`)
	if err != nil {
		return nil, err
	}

	return &indexStatements{upsert: upsert, drop: drop, add: add, stored: stored}, nil
}

// surviving answers the text a tombstone has to keep: a deletion carries no
// words, and search is the only way to find a message Slack no longer serves.
func (s *indexStatements) surviving(atTeam, atTS string) string {
	var text sql.NullString

	if err := s.stored.QueryRowContext(context.Background(), atTeam, atTS).Scan(&text); err != nil {
		return ""
	}

	return text.String
}

func (s *indexStatements) write(team string, records []ArchivedMessage) error {
	ctx := context.Background()

	for _, record := range records {
		deleted := 0
		if record.DeletedAt != "" {
			deleted = 1
		}

		var replyCount any
		if record.ReplyCount != nil {
			replyCount = *record.ReplyCount
		}

		var text any
		if record.Text != nil {
			text = *record.Text
		}

		if _, err := s.upsert.ExecContext(ctx, team, record.TS, record.Chan, nullable(record.ChannelName),
			nullable(record.User), nullable(record.Username), text, nullable(record.ThreadTS),
			nullable(record.Subtype), nullable(record.EditedTS), deleted, replyCount); err != nil {
			return err
		}

		if _, err := s.drop.ExecContext(ctx, team, record.TS); err != nil {
			return err
		}

		indexed := record.Body()

		if indexed == "" && record.DeletedAt != "" {
			indexed = s.surviving(team, record.TS)
		}

		if indexed != "" {
			if _, err := s.add.ExecContext(ctx, indexed, record.TS, team, record.Chan); err != nil {
				return err
			}
		}
	}

	return nil
}

// Search is full text, ranked, over every workspace in the index.
func (i *Index) Search(params SearchParams) ([]SearchHit, error) {
	match := ToFTSQuery(params.Query)
	if match == "" {
		return nil, nil
	}

	if err := i.Open(); err != nil {
		return nil, err
	}

	where := []string{"messages_fts MATCH ?"}
	args := []any{match}

	if params.Team != "" {
		where = append(where, "messages_fts.team = ?")
		args = append(args, params.Team)
	}

	if params.Channel != "" {
		where = append(where, "messages_fts.channel = ?")
		args = append(args, params.Channel)
	}

	if params.ChannelName != "" {
		where = append(where, "lower(m.channel_name) = lower(?)")
		args = append(args, params.ChannelName)
	}

	if !params.IncludeDeleted {
		where = append(where, "m.deleted = 0")
	}

	args = append(args, limitOr(params.Limit))

	query := searchPrefix + strings.Join(where, " AND ") + searchSuffix //nolint:gosec // fixed conditions, bound values

	rows, err := i.db.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	return scanHits(rows)
}

// List is a timeline across every conversation.
func (i *Index) List(params ListParams) ([]SearchHit, error) {
	if err := i.Open(); err != nil {
		return nil, err
	}

	where, args := listConditions(params)

	args = append(args, limitOr(params.Limit))

	order := "ASC"
	if params.Newest {
		order = "DESC"
	}

	clause := ""
	if len(where) > 0 {
		clause = "WHERE " + strings.Join(where, " AND ")
	}

	query := listPrefix + clause + listMiddle + order + listSuffix

	rows, err := i.db.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}

	defer func() { _ = rows.Close() }()

	return scanHits(rows)
}

func listConditions(params ListParams) ([]string, []any) {
	var where []string

	var args []any

	if params.Team != "" {
		where = append(where, "team = ?")
		args = append(args, params.Team)
	}

	if params.Channel != "" {
		where = append(where, "channel = ?")
		args = append(args, params.Channel)
	}

	if params.ChannelName != "" {
		where = append(where, "lower(channel_name) = lower(?)")
		args = append(args, params.ChannelName)
	}

	if len(params.Authors) > 0 {
		placeholders := make([]string, len(params.Authors))
		for index := range params.Authors {
			placeholders[index] = "?"
			args = append(args, params.Authors[index])
		}

		where = append(where, "user IN ("+strings.Join(placeholders, ", ")+")")
	}

	if !params.IncludeDeleted {
		where = append(where, "deleted = 0")
	}

	// A reply lands on its parent's day, so a timeline of what was said wants roots.
	if !params.IncludeReplies {
		where = append(where, "thread_ts IS NULL")
	}

	if params.Oldest != "" {
		where = append(where, "CAST(ts AS REAL) > CAST(? AS REAL)")
		args = append(args, params.Oldest)
	}

	if params.Latest != "" {
		where = append(where, "CAST(ts AS REAL) < CAST(? AS REAL)")
		args = append(args, params.Latest)
	}

	return where, args
}

// Stats answers how much history is filed, and how far back it goes.
func (i *Index) Stats(team string) (Stats, error) {
	if err := i.Open(); err != nil {
		return Stats{}, err
	}

	query := `
      SELECT count(*) AS messages,
             sum(CASE WHEN deleted = 1 THEN 1 ELSE 0 END) AS deleted,
             count(DISTINCT channel) AS channels,
             min(ts) AS oldest,
             max(ts) AS newest
      FROM messages`

	var args []any

	if team != "" {
		query += " WHERE team = ?"

		args = append(args, team)
	}

	var (
		stats          Stats
		deleted        sql.NullInt64
		oldest, newest sql.NullString
	)

	row := i.db.QueryRowContext(context.Background(), query, args...)
	if err := row.Scan(&stats.Messages, &deleted, &stats.Channels, &oldest, &newest); err != nil {
		return Stats{}, err
	}

	stats.Deleted = int(deleted.Int64)
	stats.Oldest = oldest.String
	stats.Newest = newest.String

	return stats, nil
}

// Reset empties the index without touching the archive.
func (i *Index) Reset() error {
	if err := i.Open(); err != nil {
		return err
	}

	_, err := i.db.ExecContext(context.Background(), `DELETE FROM messages; DELETE FROM messages_fts;`)

	return err
}

// ToFTSQuery turns free text into a safe FTS5 query: every token becomes a quoted
// term joined with AND, so a stray `*`, `:` or `-` cannot raise a SQL error.
func ToFTSQuery(query string) string {
	stripped := regexp.MustCompile(`["*:^()-]`)
	boolean := regexp.MustCompile(`(?i)^(and|or|not|near)$`)

	tokens := make([]string, 0, 8)

	for token := range strings.FieldsSeq(query) {
		cleaned := stripped.ReplaceAllString(token, "")
		if cleaned == "" || boolean.MatchString(cleaned) {
			continue
		}

		tokens = append(tokens, `"`+cleaned+`"`)
	}

	return strings.Join(tokens, " AND ")
}

// RebuildIndex drops the index and rebuilds it from the JSONL, which is what
// makes a damaged index cost time rather than history.
func RebuildIndex(store *Store, index *Index) (int, error) {
	if err := index.Reset(); err != nil {
		return 0, err
	}

	total := 0

	teams, err := store.Teams()
	if err != nil {
		return 0, err
	}

	for _, team := range teams {
		channels, err := store.ChannelIDs(team)
		if err != nil {
			return 0, err
		}

		for _, channel := range channels {
			records, err := store.Read(team, channel)
			if err != nil {
				return 0, err
			}

			if err := index.Upsert(team, records); err != nil {
				return 0, err
			}

			total += len(records)
		}
	}

	return total, nil
}

// scanHits reads both paths' columns, which are now the same list: a hit from a
// search and a hit from a timeline are the same answer, so nothing downstream has to
// ask where it came from before deciding whether a message has replies.
func scanHits(rows *sql.Rows) ([]SearchHit, error) {
	hits := make([]SearchHit, 0, 32)

	for rows.Next() {
		var (
			hit         SearchHit
			channelName sql.NullString
			user        sql.NullString
			username    sql.NullString
			text        sql.NullString
			threadTS    sql.NullString
			deleted     int
			replyCount  sql.NullInt64
		)

		err := rows.Scan(&hit.Team, &hit.Channel, &channelName, &hit.TS, &user, &username,
			&text, &threadTS, &deleted, &replyCount)
		if err != nil {
			return nil, err
		}

		hit.ChannelName = channelName.String
		hit.User = user.String
		hit.Username = username.String
		hit.Text = redact.Text(text.String)
		hit.ThreadTS = threadTS.String
		hit.Deleted = deleted == 1

		if replyCount.Valid {
			value := int(replyCount.Int64)
			hit.ReplyCount = &value
		}

		hits = append(hits, hit)
	}

	return hits, rows.Err()
}

func nullable(value string) any {
	if value == "" {
		return nil
	}

	return value
}

func limitOr(limit int) int {
	if limit <= 0 {
		return 20
	}

	return limit
}

// ErrNoIndex is what a caller sees when the index file cannot be opened at all.
var ErrNoIndex = errors.New("the archive index is not available")

// Ensure makes sure the index file exists and is private, which is what a read
// tool does before it writes back what it fetched.
func (i *Index) Ensure() error {
	if err := i.Open(); err != nil {
		return fmt.Errorf("%w: %w", ErrNoIndex, err)
	}

	return platform.Harden(i.file)
}
