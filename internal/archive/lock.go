// Package archive is the local copy of Slack history: an append-only JSONL file
// per channel, an SQLite index derived from it, and the schema version that lets
// a newer build read what an older one wrote.
package archive

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// maxLockAge is when a lock is taken over whatever the owner file says: an
	// archive must never stay blocked because a process died months ago.
	maxLockAge = 6 * time.Hour
	// ownerGrace covers the gap between `mkdir` and the owner file being written.
	// Without it a lock that is being created looks abandoned.
	ownerGrace = 5 * time.Second
)

// LockOwner says who holds a lock, which is what makes a crashed holder
// recoverable by checking whether that pid is still alive.
type LockOwner struct {
	PID       int       `json:"pid"`
	Host      string    `json:"host"`
	StartedAt time.Time `json:"-"`
	started   string
}

// MarshalJSON keeps the timestamp in the shape the TypeScript build wrote.
func (o LockOwner) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		PID       int    `json:"pid"`
		Host      string `json:"host"`
		StartedAt string `json:"started_at"`
	}{o.PID, o.Host, o.startedAt()})
}

// UnmarshalJSON reads both this build's shape and the older one.
func (o *LockOwner) UnmarshalJSON(raw []byte) error {
	var parsed struct {
		PID       int    `json:"pid"`
		Host      string `json:"host"`
		StartedAt string `json:"started_at"`
	}

	if err := json.Unmarshal(raw, &parsed); err != nil {
		return err
	}

	o.PID = parsed.PID
	o.Host = parsed.Host
	o.started = parsed.StartedAt

	if when, err := time.Parse(time.RFC3339Nano, parsed.StartedAt); err == nil {
		o.StartedAt = when
	}

	return nil
}

func (o LockOwner) startedAt() string {
	if o.StartedAt.IsZero() {
		return o.started
	}

	return o.StartedAt.UTC().Format("2006-01-02T15:04:05.000Z")
}

// Lock is a mutual exclusion for anything that must not interleave: the archive
// sync and the interactive login.
//
// It is a directory rather than a file lock: `mkdir` is atomic on every
// filesystem this runs on, and the owner file makes a crashed holder recoverable
// without leaving the archive blocked forever.
type Lock struct {
	dir  string
	held bool
}

// NewLock points at the lock directory.
func NewLock(dir string) *Lock {
	return &Lock{dir: dir}
}

// NewSyncLock is the archive's writer lock, at the root of the archive directory.
func NewSyncLock(archiveRoot string) *Lock {
	return NewLock(filepath.Join(archiveRoot, ".sync.lock"))
}

// Acquire takes the lock, taking over one whose holder is gone.
func (l *Lock) Acquire() (bool, error) {
	created, err := l.tryCreate()
	if err != nil || created {
		return created, err
	}

	owner, err := l.Owner()
	if err != nil {
		return false, err
	}

	if owner == nil {
		fresh, err := l.justCreated()
		if err != nil {
			return false, err
		}

		if fresh {
			return false, nil
		}

		if err := l.takeOver(); err != nil {
			return false, err
		}

		return l.tryCreate()
	}

	if !isStale(*owner) {
		return false, nil
	}

	if err := l.takeOver(); err != nil {
		return false, err
	}

	return l.tryCreate()
}

// Owner is who holds the lock, or nil when nobody does.
func (l *Lock) Owner() (*LockOwner, error) {
	raw, err := os.ReadFile(filepath.Join(l.dir, "owner.json"))
	if err != nil {
		return nil, nil //nolint:nilerr // no owner file means no lock, which is an answer
	}

	var owner LockOwner
	if err := json.Unmarshal(raw, &owner); err != nil {
		return nil, nil //nolint:nilerr // a garbled owner file says the same thing
	}

	return &owner, nil
}

// Release gives the lock up. A lock this process does not hold is left alone.
func (l *Lock) Release() error {
	if !l.held {
		return nil
	}

	l.held = false

	return os.RemoveAll(l.dir)
}

func (l *Lock) justCreated() (bool, error) {
	info, err := os.Stat(l.dir)
	if err != nil {
		return false, nil //nolint:nilerr // no directory means nobody created the lock
	}

	return time.Since(info.ModTime()) < ownerGrace, nil
}

// takeOver moves the directory aside, which is atomic: two processes that both
// decide the lock is stale cannot delete each other's fresh one, because only the
// rename's winner takes it over.
func (l *Lock) takeOver() error {
	moved := fmt.Sprintf("%s.stale.%d", l.dir, os.Getpid())

	if err := os.RemoveAll(moved); err != nil {
		return err
	}

	if err := os.Rename(l.dir, moved); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil
	}

	return os.RemoveAll(moved)
}

func (l *Lock) tryCreate() (bool, error) {
	if err := os.MkdirAll(filepath.Dir(l.dir), 0o700); err != nil {
		return false, err
	}

	if err := os.Mkdir(l.dir, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}

		return false, err
	}

	host, _ := os.Hostname()

	owner := LockOwner{PID: os.Getpid(), Host: host, StartedAt: time.Now()}

	encoded, err := json.Marshal(owner)
	if err != nil {
		return false, err
	}

	file := filepath.Join(l.dir, "owner.json")

	if err := os.WriteFile(file, append(encoded, '\n'), 0o600); err != nil {
		return false, err
	}

	l.held = true

	return true, nil
}

func isStale(owner LockOwner) bool {
	if !owner.StartedAt.IsZero() && time.Since(owner.StartedAt) > maxLockAge {
		return true
	}

	host, _ := os.Hostname()
	if owner.Host != host {
		return false
	}

	return !processAlive(owner.PID)
}
