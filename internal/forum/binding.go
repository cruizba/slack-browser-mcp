package forum

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/archive"
	"github.com/cruizba/slack-browser-mcp/internal/failure"
	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

// ForumFileVersion is the shape of `forum.json`.
const ForumFileVersion = 1

// lockWait is how long a writer waits for another one before giving up.
const lockWait = 2 * time.Second

// Binding is one channel this machine talks on.
type Binding struct {
	Workspace string `json:"workspace"`
	Channel   string `json:"channel"`
	Name      string `json:"name"`
	Private   bool   `json:"private"`
	SinceTS   string `json:"since_ts"`
	AddedAt   string `json:"added_at"`
	// Sign is the AI-assisted header. Off for the forum: the envelope already names
	// the sender, and the header's blocks make Slack flatten the message text, which
	// is where the envelope itself lives.
	Sign bool `json:"sign"`
	// Announce means this machine announces its presence here without being asked.
	// Off is read-only for this machine.
	Announce bool `json:"announce"`
}

// StoredIdentity is one local agent, as `register` filed it.
type StoredIdentity struct {
	Name         string   `json:"name"`
	Host         string   `json:"host"`
	Capabilities []string `json:"capabilities"`
	RegisteredAt string   `json:"registered_at"`
}

// Label is how this identity is addressed.
func (i StoredIdentity) Label() string {
	return LabelOf(i.Name, i.Host)
}

// Counter is kept so a file written by an earlier build still parses; nothing counts
// posts any more.
type Counter struct {
	Hour  string `json:"hour"`
	Count int    `json:"count"`
}

// File is `forum.json`: which channels this machine talks on, who each local
// agent is, how big the fences are, and how far each identity has read. Nothing in
// it is a credential; losing the file costs a `register` and an `add`, not a login.
type File struct {
	Version         int                          `json:"version"`
	Identities      map[string]StoredIdentity    `json:"identities"`
	DefaultIdentity string                       `json:"default_identity,omitempty"`
	Channels        []Binding                    `json:"channels"`
	Limits          *Limits                      `json:"limits,omitempty"`
	Cursors         map[string]map[string]string `json:"cursors"`
	Ignored         []string                     `json:"ignored"`
	Posting         map[string]Counter           `json:"posting"`
}

// EmptyForum is what a machine that has never bound a channel has.
func EmptyForum() File {
	return File{
		Version:    ForumFileVersion,
		Identities: map[string]StoredIdentity{},
		Channels:   []Binding{},
		Cursors:    map[string]map[string]string{},
		Ignored:    []string{},
		Posting:    map[string]Counter{},
	}
}

// BindingKey identifies one channel of one workspace.
func BindingKey(workspace, channel string) string {
	return workspace + ":" + channel
}

// Key is how this binding is identified in the file.
func (b Binding) Key() string {
	return BindingKey(b.Workspace, b.Channel)
}

// LimitsOf folds the file's clocks over the defaults.
func LimitsOf(file File) Limits {
	return LimitsWith(file.Limits)
}

// Store is the file, read and written under a lock.
type Store struct {
	file string
}

// NewStore points at the forum file.
func NewStore(file string) *Store {
	return &Store{file: file}
}

// Path is where the file lives, for a caller that has to name it in an error.
func (s *Store) Path() string {
	return s.file
}

// Read answers the file. A missing or empty one is a machine that has never bound a
// channel, which is not an error.
func (s *Store) Read() (File, error) {
	raw, err := os.ReadFile(s.file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return EmptyForum(), nil
		}

		return File{}, err
	}

	if bytes.TrimSpace(raw) == nil {
		return EmptyForum(), nil
	}

	file := EmptyForum()

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&file); err != nil {
		return File{}, &failure.StoreError{Message: s.file + " is not valid JSON. Fix it or delete it."}
	}

	if file.Version != ForumFileVersion {
		return File{}, &failure.StoreError{Message: s.file + " is not a valid forum file (version: expected " +
			strconv.Itoa(ForumFileVersion) + ")."}
	}

	if file.Identities == nil {
		file.Identities = map[string]StoredIdentity{}
	}

	if file.Cursors == nil {
		file.Cursors = map[string]map[string]string{}
	}

	if file.Posting == nil {
		file.Posting = map[string]Counter{}
	}

	for key, identity := range file.Identities {
		if problem := validateIdentity(identity); problem != "" {
			return File{}, &failure.StoreError{Message: s.file + " is not a valid forum file (identities." +
				key + ": " + problem + ")."}
		}
	}

	for index, binding := range file.Channels {
		if problem := validateBinding(binding); problem != "" {
			return File{}, &failure.StoreError{Message: s.file + " is not a valid forum file (channels." +
				strconv.Itoa(index) + ": " + problem + ")."}
		}
	}

	return file, nil
}

func validateIdentity(identity StoredIdentity) string {
	if problem := lengthProblem("name", identity.Name, 1, MaxNameChars); problem != "" {
		return problem
	}

	if problem := lengthProblem("host", identity.Host, 1, MaxNameChars); problem != "" {
		return problem
	}

	for _, tag := range identity.Capabilities {
		if problem := lengthProblem("capabilities", tag, 1, MaxTagChars); problem != "" {
			return problem
		}
	}

	if identity.RegisteredAt == "" {
		return "registered_at is required"
	}

	return ""
}

func validateBinding(binding Binding) string {
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "workspace", value: binding.Workspace},
		{name: "channel", value: binding.Channel},
		{name: "name", value: binding.Name},
		{name: "since_ts", value: binding.SinceTS},
		{name: "added_at", value: binding.AddedAt},
	} {
		if field.value == "" {
			return field.name + " is required"
		}
	}

	return ""
}

// Write replaces the file atomically at 0600: it is the user's, and a half-written
// one would lose every binding at once.
func (s *Store) Write(file File) error {
	if err := os.MkdirAll(filepath.Dir(s.file), 0o700); err != nil {
		return err
	}

	encoded, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}

	temporary := fmt.Sprintf("%s.%d.tmp", s.file, os.Getpid())

	handle, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // the path is the forum file
	if err != nil {
		return err
	}

	if _, err := handle.Write(append(encoded, '\n')); err != nil {
		_ = handle.Close()

		return err
	}

	if err := handle.Sync(); err != nil {
		_ = handle.Close()

		return err
	}

	if err := handle.Close(); err != nil {
		return err
	}

	if err := platform.Harden(temporary); err != nil {
		return err
	}

	return os.Rename(temporary, s.file)
}

// ResolveIdentity answers which stored identity this process is. The key is the MCP
// client name, so two agents on one machine keep separate names and cursors.
func (s *Store) ResolveIdentity(file File, clientName string, env platform.Env) (string, *StoredIdentity) {
	// An explicit name is a request for that key, not a hint: borrowing the only
	// identity on the machine would make the first register of a second agent
	// overwrite the first one.
	fromEnv, _ := platform.Lookup(env, AgentEnv)

	named := NormalizeName(fromEnv)
	if named == "" {
		named = NormalizeName(clientName)
	}

	key := AgentName(env, clientName, file.DefaultIdentity)

	if identity, known := file.Identities[key]; known {
		return key, &identity
	}

	if named != "" {
		return key, nil
	}

	// With no name to key on (a plain CLI call), one registered identity is the answer
	// for any client.
	if len(file.Identities) == 1 {
		for only, identity := range file.Identities {
			stored := identity

			return only, &stored
		}
	}

	return key, nil
}

// Register files an identity and makes it the default when there is none.
func (s *Store) Register(key string, identity StoredIdentity) (File, error) {
	return s.mutate(func(file *File) bool {
		file.Identities[key] = identity

		if file.DefaultIdentity == "" {
			file.DefaultIdentity = key
		}

		return true
	})
}

// Unregister drops the identity and retires the label, so its own inbox stops
// filling up.
func (s *Store) Unregister(key, label string) (bool, error) {
	removed := false

	_, err := s.mutate(func(file *File) bool {
		if _, known := file.Identities[key]; !known {
			return false
		}

		delete(file.Identities, key)

		if file.DefaultIdentity == key {
			remaining := make([]string, 0, len(file.Identities))
			for candidate := range file.Identities {
				remaining = append(remaining, candidate)
			}

			sort.Strings(remaining)

			if len(remaining) > 0 {
				file.DefaultIdentity = remaining[0]
			} else {
				file.DefaultIdentity = ""
			}
		}

		if !slicesContains(file.Ignored, label) {
			file.Ignored = append(file.Ignored, label)
		}

		removed = true

		return true
	})

	return removed, err
}

// Bind adds a channel, answering whether it was new.
func (s *Store) Bind(binding Binding) (bool, error) {
	added := false

	_, err := s.mutate(func(file *File) bool {
		for _, entry := range file.Channels {
			if entry.Key() == binding.Key() {
				return false
			}
		}

		file.Channels = append(file.Channels, binding)
		added = true

		return true
	})

	return added, err
}

// Unbind forgets a channel.
func (s *Store) Unbind(workspace, channel string) (bool, error) {
	removed := false
	key := BindingKey(workspace, channel)

	_, err := s.mutate(func(file *File) bool {
		kept := make([]Binding, 0, len(file.Channels))

		for _, entry := range file.Channels {
			if entry.Key() == key {
				continue
			}

			kept = append(kept, entry)
		}

		if len(kept) == len(file.Channels) {
			return false
		}

		file.Channels = kept
		removed = true

		return true
	})

	return removed, err
}

// Ignore retires a label locally, so its tasks stop showing up in the inbox.
func (s *Store) Ignore(label string) (bool, error) {
	added := false

	_, err := s.mutate(func(file *File) bool {
		if slicesContains(file.Ignored, label) {
			return false
		}

		file.Ignored = append(file.Ignored, label)
		added = true

		return true
	})

	return added, err
}

// Unignore brings a label back.
func (s *Store) Unignore(label string) (bool, error) {
	removed := false

	_, err := s.mutate(func(file *File) bool {
		kept := make([]string, 0, len(file.Ignored))

		for _, entry := range file.Ignored {
			if entry == label {
				continue
			}

			kept = append(kept, entry)
		}

		if len(kept) == len(file.Ignored) {
			return false
		}

		file.Ignored = kept
		removed = true

		return true
	})

	return removed, err
}

// mutate is read-modify-write under a lock: a setup script that registers two agents
// at once would otherwise have both read the same snapshot, and the second write
// would silently drop the first one's identity.
func (s *Store) mutate(change func(file *File) bool) (File, error) {
	lock := archive.NewLock(s.file + ".lock")

	deadline := time.Now().Add(lockWait)

	for {
		acquired, err := lock.Acquire()
		if err != nil {
			return File{}, err
		}

		if acquired {
			break
		}

		if time.Now().After(deadline) {
			return File{}, &failure.StoreError{Message: s.file +
				" is being edited by another process; try again in a moment."}
		}

		time.Sleep(25 * time.Millisecond)
	}

	defer func() { _ = lock.Release() }()

	file, err := s.Read()
	if err != nil {
		return File{}, err
	}

	if !change(&file) {
		return file, nil
	}

	if err := s.Write(file); err != nil {
		return File{}, err
	}

	return file, nil
}

func slicesContains(values []string, wanted string) bool {
	return slices.Contains(values, wanted)
}
