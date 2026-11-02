package archive

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/failure"
	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

// SchemaVersion is the layout this build writes. Bump it when a reader that
// predates the change would misread stored data.
const SchemaVersion = 2

// SchemaMarker is the file that says which layout an archive is in.
type SchemaMarker struct {
	Version   int    `json:"version"`
	UpdatedAt string `json:"updated_at"`
}

// SchemaStatus is what a migration did, or why it did nothing.
type SchemaStatus struct {
	From    int
	To      int
	Applied []int
	// Locked is set when another process held the sync lock; reads still work.
	Locked bool
}

func markerPath(root string) string {
	return filepath.Join(root, "schema.json")
}

// normalizeRecord folds a record written before files carried ids into the
// current shape, and drops the nulls Slack sends for absent optional fields where
// this code expects a missing key.
func normalizeRecord(record map[string]any) map[string]any {
	next := make(map[string]any, len(record))

	for key, value := range record {
		if value == nil {
			continue
		}

		next[key] = value
	}

	if files, ok := next["files"].([]any); ok {
		folded := make([]any, 0, len(files))

		for _, entry := range files {
			if name, isString := entry.(string); isString {
				folded = append(folded, map[string]any{"name": name})

				continue
			}

			folded = append(folded, entry)
		}

		if len(folded) > 0 {
			next["files"] = folded
		} else {
			delete(next, "files")
		}
	}

	return next
}

func migrateRecords(root string, log func(string)) error {
	store := NewStore(root)

	teams, err := store.Teams()
	if err != nil {
		return err
	}

	for _, team := range teams {
		channels, err := store.ChannelIDs(team)
		if err != nil {
			return err
		}

		for _, channel := range channels {
			if err := migrateChannel(store, team, channel, log); err != nil {
				return err
			}
		}
	}

	// The FTS index is derived data and `sync` only adds new rows to it, so a
	// migration has to rebuild it: dropping it would leave search blind until
	// somebody remembered `archive reindex`.
	if err := os.Remove(indexPathFor(root)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	index := NewIndex(indexPathFor(root))

	defer func() { _ = index.Close() }()

	indexed, err := RebuildIndex(store, index)
	if err != nil {
		return err
	}

	log("rebuilt the FTS index: " + strconv.Itoa(indexed) + " records")

	return nil
}

func migrateChannel(store *Store, team, channel string, log func(string)) error {
	file, err := store.ChannelFile(team, channel)
	if err != nil {
		return err
	}

	raw, err := os.ReadFile(file) //nolint:gosec // the path is built from validated ids
	if err != nil {
		return err
	}

	rewritten, corrupt, changed := normalizeLines(string(raw))

	if !changed {
		return nil
	}

	if len(corrupt) > 0 {
		if err := appendLines(file+".corrupt", corrupt); err != nil {
			return err
		}

		log(file + ": " + strconv.Itoa(len(corrupt)) + " unreadable line(s) moved to " + file + ".corrupt")
	}

	if err := replaceLines(file, rewritten); err != nil {
		return err
	}

	log(file + ": " + strconv.Itoa(len(rewritten)) + " records normalised")

	return platform.Harden(file)
}

// normalizeLines rewrites what a migration would change and reports whether any
// line actually changed, so an archive already in the current shape is never
// rewritten.
func normalizeLines(raw string) (rewritten, corrupt []string, changed bool) {
	for line := range strings.SplitSeq(raw, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}

		var parsed map[string]any
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			corrupt = append(corrupt, line)
			changed = true

			continue
		}

		before, _ := json.Marshal(parsed)
		after, _ := json.Marshal(normalizeRecord(parsed))

		if string(before) != string(after) {
			changed = true
		}

		rewritten = append(rewritten, string(after))
	}

	return rewritten, corrupt, changed
}

func appendLines(file string, lines []string) error {
	handle, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // the channel file plus a fixed suffix
	if err != nil {
		return err
	}

	if _, err := handle.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		_ = handle.Close()

		return err
	}

	return handle.Close()
}

func replaceLines(file string, lines []string) error {
	temporary := file + ".tmp"

	if err := os.WriteFile(temporary, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return err
	}

	return os.Rename(temporary, file)
}

func readMarker(root string) *SchemaMarker {
	raw, err := os.ReadFile(markerPath(root))
	if err != nil {
		return nil
	}

	var marker SchemaMarker
	if err := json.Unmarshal(raw, &marker); err != nil {
		return nil
	}

	return &marker
}

func writeMarker(root string, version int) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}

	encoded, err := json.Marshal(SchemaMarker{
		Version:   version,
		UpdatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	})
	if err != nil {
		return err
	}

	temporary := markerPath(root) + ".tmp"

	if err := os.WriteFile(temporary, append(encoded, '\n'), 0o600); err != nil {
		return err
	}

	if err := os.Rename(temporary, markerPath(root)); err != nil {
		return err
	}

	return platform.Harden(markerPath(root))
}

// currentVersion answers which layout an archive is in. Data written before the
// marker existed is version 1: the layout had not changed since, it just had no
// way to say so.
func currentVersion(root string) int {
	if marker := readMarker(root); marker != nil {
		return marker.Version
	}

	store := NewStore(root)

	teams, err := store.Teams()
	if err == nil {
		for _, team := range teams {
			if channels, err := store.ChannelIDs(team); err == nil && len(channels) > 0 {
				return 1
			}
		}
	}

	return SchemaVersion
}

// EnsureSchema brings the archive up to this build's layout, under the same lock
// the sync holds: a migration rewrites channel files, so it must not run next to
// an append.
//
// A migration is idempotent, so a crash halfway converges on the next run, and it
// only rewrites a channel file when a line actually changes.
func EnsureSchema(root string, log func(string)) (SchemaStatus, error) {
	from := currentVersion(root)

	if from > SchemaVersion {
		return SchemaStatus{From: from, To: SchemaVersion}, &failure.SchemaError{
			Message: "The archive at " + root + " was written with schema " + strconv.Itoa(from) +
				", newer than this build understands (" + strconv.Itoa(SchemaVersion) + ").",
			Hint: "Update slack-browser before reading or syncing: a newer version wrote that data.",
		}
	}

	if from == SchemaVersion {
		if readMarker(root) == nil {
			if err := writeMarker(root, from); err != nil {
				return SchemaStatus{From: from, To: from}, err
			}
		}

		return SchemaStatus{From: from, To: from}, nil
	}

	lock := NewSyncLock(root)

	acquired, err := lock.Acquire()
	if err != nil {
		return SchemaStatus{From: from, To: from}, err
	}

	if !acquired {
		return SchemaStatus{From: from, To: from, Locked: true}, nil
	}

	defer func() { _ = lock.Release() }()

	applied := make([]int, 0, 1)

	if from < 2 {
		log("migrating archive schema 1 -> 2: attachments carry their id and absent fields are omitted")

		if err := migrateRecords(root, log); err != nil {
			return SchemaStatus{From: from, To: from, Applied: applied}, err
		}

		applied = append(applied, 2)
	}

	if err := writeMarker(root, SchemaVersion); err != nil {
		return SchemaStatus{From: from, To: SchemaVersion, Applied: applied}, err
	}

	return SchemaStatus{From: from, To: SchemaVersion, Applied: applied}, nil
}

// indexPathFor is the index beside an archive root (the two are siblings under
// the data directory), so a migration run against another root leaves the real
// index alone.
func indexPathFor(root string) string {
	return filepath.Join(filepath.Dir(root), "archive.sqlite")
}
