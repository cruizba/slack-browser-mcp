package archive

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

// ArchivedFile is an attachment as the archive keeps it: enough to name it, not
// the bytes, which live beside the JSONL.
type ArchivedFile struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	Title    string `json:"title,omitempty"`
	Mimetype string `json:"mimetype,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

// ArchivedMessage is a Slack message as the archive stores it.
type ArchivedMessage struct {
	TS   string `json:"ts"`
	Chan string `json:"channel"`
	// ChannelName is a channel's name or, for a DM, the peer's user id.
	ChannelName string `json:"channel_name,omitempty"`
	// Source is `search` for a message filed from Slack search rather than a
	// channel fetch: it is searchable, but it is not a contiguous window, so
	// channel history must not be served from it.
	Source   string `json:"source,omitempty"`
	User     string `json:"user,omitempty"`
	Username string `json:"username,omitempty"`
	BotID    string `json:"bot_id,omitempty"`
	// Text and ReplyCount are pointers because absent and empty are different
	// answers: Slack sends `text: ""` for some subtypes, and `reply_count: 0` is a
	// thread nobody answered, which is not the same as a message Slack said nothing
	// about.
	Text       *string        `json:"text,omitempty"`
	ThreadTS   string         `json:"thread_ts,omitempty"`
	Subtype    string         `json:"subtype,omitempty"`
	EditedTS   string         `json:"edited_ts,omitempty"`
	ReplyCount *int           `json:"reply_count,omitempty"`
	Files      []ArchivedFile `json:"files,omitempty"`
	// Forum is the envelope a `forum-*` attachment carried, kept inline so a read
	// never needs the file back: `inbox --offline` and the ledger rebuild have to
	// work with the channel file alone.
	Forum     string `json:"forum,omitempty"`
	SyncedAt  string `json:"synced_at"`
	DeletedAt string `json:"deleted_at,omitempty"`
}

// ChannelState is what the sync remembers about one channel, which is what a
// backfill continues from.
type ChannelState struct {
	NewestTS      string   `json:"newest_ts,omitempty"`
	OldestTS      string   `json:"oldest_ts,omitempty"`
	LastSync      string   `json:"last_sync,omitempty"`
	LastReconcile string   `json:"last_reconcile,omitempty"`
	LastError     string   `json:"last_error,omitempty"`
	PendingThread []string `json:"pending_threads,omitempty"`
}

// TeamState is one workspace's slice of the sync state.
type TeamState struct {
	LastRun  string                  `json:"last_run,omitempty"`
	Channels map[string]ChannelState `json:"channels"`
}

// SyncState is what the sync keeps between runs: cursors, nothing to read.
type SyncState struct {
	Version int                   `json:"version"`
	Teams   map[string]*TeamState `json:"teams"`
}

func emptyState() SyncState {
	return SyncState{Version: 1, Teams: map[string]*TeamState{}}
}

// Store is the append-only archive: one JSONL file per channel. Messages are
// immutable in Slack apart from edits and deletions, so the file is an event log
// where the last line for a `ts` wins, and a deletion is a line with `deleted_at`
// rather than a removed line.
type Store struct {
	Root string
}

// NewStore points at the archive root.
func NewStore(root string) *Store {
	return &Store{Root: root}
}

func (s *Store) statePath() string {
	return filepath.Join(s.Root, "state.json")
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (s *Store) teamDir(teamID string) (string, error) {
	if !idPattern.MatchString(teamID) {
		return "", errors.New("Invalid team id: " + teamID)
	}

	return filepath.Join(s.Root, teamID), nil
}

// ChannelFile is where one channel's history lives.
func (s *Store) ChannelFile(teamID, channelID string) (string, error) {
	dir, err := s.teamDir(teamID)
	if err != nil {
		return "", err
	}

	if !idPattern.MatchString(channelID) {
		return "", errors.New("Invalid channel id: " + channelID)
	}

	return filepath.Join(dir, channelID+".jsonl"), nil
}

// Append adds records to a channel. An empty batch writes nothing, so a sync
// that found nothing new does not touch the file.
func (s *Store) Append(teamID, channelID string, records []ArchivedMessage) error {
	if len(records) == 0 {
		return nil
	}

	dir, err := s.teamDir(teamID)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	if err := platform.HardenDir(dir); err != nil {
		return err
	}

	file, err := s.ChannelFile(teamID, channelID)
	if err != nil {
		return err
	}

	var payload strings.Builder

	for _, record := range records {
		encoded, err := json.Marshal(record)
		if err != nil {
			return err
		}

		payload.Write(encoded)
		payload.WriteByte('\n')
	}

	handle, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // the path is built from validated ids
	if err != nil {
		return err
	}

	if _, err := handle.WriteString(payload.String()); err != nil {
		_ = handle.Close()

		return err
	}

	if err := handle.Close(); err != nil {
		return err
	}

	return platform.Harden(file)
}

// Read answers a channel's messages, one per ts, oldest first. A line torn by a
// crash mid-append costs that line and not the channel: the next sync appends the
// message again.
func (s *Store) Read(teamID, channelID string) ([]ArchivedMessage, error) {
	file, err := s.ChannelFile(teamID, channelID)
	if err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(file) //nolint:gosec // the path is built from validated ids
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}

		return nil, err
	}

	records := make([]ArchivedMessage, 0, 256)

	for line := range strings.SplitSeq(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}

		var record ArchivedMessage
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}

		records = append(records, record)
	}

	return fold(records), nil
}

// fold keeps the last line for each ts and sorts the result oldest first. A later
// line without `deleted_at` or `source` clears them: those markers describe one
// event, not the message.
func fold(records []ArchivedMessage) []ArchivedMessage {
	order := make([]string, 0, len(records))
	byTS := make(map[string]ArchivedMessage, len(records))

	for _, record := range records {
		existing, seen := byTS[record.TS]
		if !seen {
			order = append(order, record.TS)
			byTS[record.TS] = record

			continue
		}

		merged := merge(existing, record)
		byTS[record.TS] = merged
	}

	folded := make([]ArchivedMessage, 0, len(order))

	for _, ts := range order {
		folded = append(folded, byTS[ts])
	}

	sort.SliceStable(folded, func(i, j int) bool {
		return numeric(folded[i].TS) < numeric(folded[j].TS)
	})

	return folded
}

// merge applies a later line over an earlier one. Deletion and origin are set
// rather than kept because each describes one event, not the message.
func merge(existing, record ArchivedMessage) ArchivedMessage {
	merged := existing

	keep(&merged.Chan, record.Chan)
	keep(&merged.ChannelName, record.ChannelName)
	keep(&merged.User, record.User)
	keep(&merged.Username, record.Username)
	keep(&merged.BotID, record.BotID)
	keep(&merged.ThreadTS, record.ThreadTS)
	keep(&merged.Subtype, record.Subtype)
	keep(&merged.EditedTS, record.EditedTS)
	keep(&merged.Forum, record.Forum)
	keep(&merged.SyncedAt, record.SyncedAt)

	if record.Text != nil {
		merged.Text = record.Text
	}

	if record.ReplyCount != nil {
		merged.ReplyCount = record.ReplyCount
	}

	if len(record.Files) > 0 {
		merged.Files = record.Files
	}

	merged.DeletedAt = record.DeletedAt
	merged.Source = record.Source

	return merged
}

// keep copies a value that the later line actually carried.
func keep[T comparable](target *T, value T) {
	var zero T
	if value != zero {
		*target = value
	}
}

// ChannelIDs is every channel file this team has.
func (s *Store) ChannelIDs(teamID string) ([]string, error) {
	dir, err := s.teamDir(teamID)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}

		return nil, err
	}

	ids := make([]string, 0, len(entries))

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}

		ids = append(ids, strings.TrimSuffix(name, ".jsonl"))
	}

	sort.Strings(ids)

	return ids, nil
}

// Teams is every workspace the archive holds.
func (s *Store) Teams() ([]string, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}

		return nil, err
	}

	teams := make([]string, 0, len(entries))

	for _, entry := range entries {
		if entry.IsDir() && idPattern.MatchString(entry.Name()) {
			teams = append(teams, entry.Name())
		}
	}

	sort.Strings(teams)

	return teams, nil
}

// LoadState answers the sync state. Cursors are advisory: a damaged file costs
// one wider backfill, while failing here would take down every read that reports
// freshness.
func (s *Store) LoadState() (SyncState, error) {
	raw, err := os.ReadFile(s.statePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return emptyState(), nil
		}

		return SyncState{}, err
	}

	var state SyncState
	if err := json.Unmarshal(raw, &state); err != nil {
		return emptyState(), nil //nolint:nilerr // cursors are advisory: a damaged file costs one wider backfill
	}

	if state.Version != 1 {
		return emptyState(), nil
	}

	if state.Teams == nil {
		state.Teams = map[string]*TeamState{}
	}

	return state, nil
}

// SaveState replaces the sync state atomically.
func (s *Store) SaveState(state SyncState) error {
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return err
	}

	if err := platform.HardenDir(s.Root); err != nil {
		return err
	}

	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	temporary := s.statePath() + ".tmp"

	if err := os.WriteFile(temporary, append(encoded, '\n'), 0o600); err != nil {
		return err
	}

	if err := os.Rename(temporary, s.statePath()); err != nil {
		return err
	}

	return platform.Harden(s.statePath())
}

func numeric(ts string) float64 {
	value, err := strconv.ParseFloat(ts, 64)
	if err != nil {
		return 0
	}

	return value
}

// Body is the message text, empty when Slack said nothing about it. The
// distinction between the two lives in the record, not in the caller.
func (m ArchivedMessage) Body() string {
	if m.Text == nil {
		return ""
	}

	return *m.Text
}

// Replies is how many answers the thread has, and whether Slack said.
func (m ArchivedMessage) Replies() (int, bool) {
	if m.ReplyCount == nil {
		return 0, false
	}

	return *m.ReplyCount, true
}
