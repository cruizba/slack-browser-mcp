package slack

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

// Kind is what a conversation is, which decides the prefix its label carries.
type Kind string

// The four kinds of conversation Slack has, which decide the label prefix.
const (
	KindChannel Kind = "channel"
	KindPrivate Kind = "private"
	KindDM      Kind = "dm"
	KindMpIM    Kind = "mpim"
)

// DirectoryMaxAge is how long a cached directory may serve before it is
// refreshed: `users.list` is far cheaper once a day than one `users.info` per
// author per read.
const DirectoryMaxAge = 24 * time.Hour

// DirectoryUser is one member, as the directory files it.
type DirectoryUser struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	RealName    string `json:"real_name,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Deleted     bool   `json:"deleted,omitempty"`
	IsBot       bool   `json:"is_bot,omitempty"`
}

// DirectoryGroup is one mention group.
type DirectoryGroup struct {
	ID     string `json:"id"`
	Handle string `json:"handle,omitempty"`
	Name   string `json:"name,omitempty"`
}

// DirectoryChannel is one conversation. A DM records the other side's user id
// as `User`, because that is the only handle saying who it is with.
type DirectoryChannel struct {
	ID         string `json:"id"`
	Kind       Kind   `json:"kind"`
	Name       string `json:"name,omitempty"`
	User       string `json:"user,omitempty"`
	NumMembers int    `json:"num_members,omitempty"`
}

// Directory is the cached map of who is who.
type Directory struct {
	Version    int                `json:"version"`
	TeamID     string             `json:"team_id"`
	SavedAt    string             `json:"saved_at"`
	Users      []DirectoryUser    `json:"users"`
	Channels   []DirectoryChannel `json:"channels"`
	Usergroups []DirectoryGroup   `json:"usergroups"`
	// DMCount is how many DMs and group DMs are held. A directory written before
	// DMs were indexed lacks it, and loading treats that as stale so the upgrade
	// lands at once instead of at the next daily refresh.
	DMCount *int `json:"dm_count,omitempty"`
}

// DirectoryClient is the slice of the API client the directory needs.
type DirectoryClient interface {
	ListUsers() ([]User, error)
	ListAllChannels() ([]Channel, error)
	ListDMChannels() ([]Channel, error)
	ListUsergroups() ([]Usergroup, error)
}

// ConversationKind infers what a channel is from the flags Slack set, and from
// the id when Slack said nothing: an id passed through rather than listed still
// says what it is in its first letter.
func ConversationKind(channel Channel) Kind {
	switch {
	case channel.IsIM:
		return KindDM
	case channel.IsMpIM:
		return KindMpIM
	case channel.IsPrivate:
		return KindPrivate
	case strings.HasPrefix(channel.ID, "D"):
		return KindDM
	case strings.HasPrefix(channel.ID, "G"):
		return KindMpIM
	default:
		return KindChannel
	}
}

// UserLabel is what a person is called here.
func UserLabel(user *DirectoryUser) string {
	if user == nil {
		return ""
	}

	for _, candidate := range []string{user.DisplayName, user.RealName, user.Name} {
		if candidate != "" {
			return candidate
		}
	}

	return user.ID
}

func groupLabel(group DirectoryGroup) string {
	for _, candidate := range []string{group.Handle, group.Name} {
		if candidate != "" {
			return candidate
		}
	}

	return group.ID
}

// Scope is what the reference resolver can answer from this directory.
func (d *Directory) Scope() *ReferenceScope {
	if d == nil {
		return nil
	}

	users := make(map[string]string, len(d.Users))
	for _, user := range d.Users {
		users[user.ID] = UserLabel(&user)
	}

	channels := make(map[string]string, len(d.Channels))
	for _, channel := range d.Channels {
		if channel.Name != "" {
			channels[channel.ID] = channel.Name
		}
	}

	groups := make(map[string]string, len(d.Usergroups))
	for _, group := range d.Usergroups {
		groups[group.ID] = groupLabel(group)
	}

	return &ReferenceScope{
		User:      func(id string) string { return users[id] },
		Channel:   func(id string) string { return channels[id] },
		Usergroup: func(id string) string { return groups[id] },
	}
}

// User answers the label of one member.
func (d *Directory) User(userID string) string {
	if d == nil {
		return ""
	}

	for i := range d.Users {
		if d.Users[i].ID == userID {
			return UserLabel(&d.Users[i])
		}
	}

	return ""
}

// Channel answers the entry filed for one conversation.
func (d *Directory) Channel(channelID string) *DirectoryChannel {
	if d == nil {
		return nil
	}

	for i := range d.Channels {
		if d.Channels[i].ID == channelID {
			return &d.Channels[i]
		}
	}

	return nil
}

// UserByHandle resolves the handle Slack builds a group DM's name from
// (`mpdm-<handles>-<n>`).
func (d *Directory) UserByHandle(handle string) *DirectoryUser {
	if d == nil {
		return nil
	}

	needle := strings.ToLower(handle)

	for i := range d.Users {
		if strings.ToLower(d.Users[i].Name) == needle && d.Users[i].Name != "" {
			return &d.Users[i]
		}
	}

	return nil
}

// UserByReference matches what a person is called around here, so `dm:Ana Ruiz`
// works, and the user id the archive stores in place of a name works too. A name
// two people share resolves to nobody: picking one would address the wrong
// person.
func (d *Directory) UserByReference(reference string) *DirectoryUser {
	if d == nil {
		return nil
	}

	needle := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(reference), "@")))
	if needle == "" {
		return nil
	}

	var matches []*DirectoryUser

	for i := range d.Users {
		user := &d.Users[i]

		if strings.ToLower(user.ID) == needle {
			matches = append(matches, user)

			continue
		}

		for _, field := range []string{user.DisplayName, user.Name, user.RealName} {
			if field != "" && strings.ToLower(field) == needle {
				matches = append(matches, user)

				break
			}
		}
	}

	if len(matches) == 1 {
		return matches[0]
	}

	return nil
}

func toDirectoryChannel(channel Channel) DirectoryChannel {
	return DirectoryChannel{
		ID:         channel.ID,
		Kind:       ConversationKind(channel),
		Name:       channel.Name,
		User:       channel.User,
		NumMembers: channel.NumMembers,
	}
}

// FetchDirectory asks Slack for everything and files it. A failed lookup
// degrades to what answered: a missing user-group list must not cost the user's
// names too. Only when both users and channels come back empty does it fail,
// because that is a directory that would render every id raw.
func FetchDirectory(client DirectoryClient, teamID string, now time.Time) (*Directory, error) {
	type usersResult struct {
		users []User
		err   error
	}

	type channelsResult struct {
		channels []Channel
		err      error
	}

	type groupsResult struct {
		groups []Usergroup
		err    error
	}

	type dmsResult struct {
		channels []Channel
		err      error
	}

	var (
		usersR    usersResult
		channelsR channelsResult
		dmsR      dmsResult
		groupsR   groupsResult
		wg        sync.WaitGroup
	)

	wg.Add(4)

	go func() {
		defer wg.Done()

		usersR.users, usersR.err = client.ListUsers()
	}()

	go func() {
		defer wg.Done()

		channelsR.channels, channelsR.err = client.ListAllChannels()
	}()

	go func() {
		defer wg.Done()

		dmsR.channels, dmsR.err = client.ListDMChannels()
	}()

	go func() {
		defer wg.Done()

		groupsR.groups, groupsR.err = client.ListUsergroups()
	}()

	wg.Wait()

	channels := append(append([]Channel{}, channelsR.channels...), dmsR.channels...)

	if len(usersR.users) == 0 && len(channels) == 0 {
		if usersR.err != nil {
			return nil, usersR.err
		}

		if channelsR.err != nil {
			return nil, channelsR.err
		}

		return nil, errors.New("Slack returned an empty directory")
	}

	users := make([]DirectoryUser, 0, len(usersR.users))
	for _, user := range usersR.users {
		users = append(users, DirectoryUser(user))
	}

	// A DM shows up in both lists once Slack adds it to the channel list too, so
	// the id, not the call, decides what the registry keeps.
	byID := make(map[string]DirectoryChannel, len(channels))
	order := make([]string, 0, len(channels))

	for _, channel := range channels {
		if _, seen := byID[channel.ID]; !seen {
			order = append(order, channel.ID)
		}

		byID[channel.ID] = toDirectoryChannel(channel)
	}

	entries := make([]DirectoryChannel, 0, len(byID))

	dms := 0

	for _, id := range order {
		entry := byID[id]
		if entry.Kind == KindDM || entry.Kind == KindMpIM {
			dms++
		}

		entries = append(entries, entry)
	}

	groups := make([]DirectoryGroup, 0, len(groupsR.groups))
	for _, group := range groupsR.groups {
		groups = append(groups, DirectoryGroup(group))
	}

	return &Directory{
		Version:    1,
		TeamID:     teamID,
		SavedAt:    now.UTC().Format(time.RFC3339Nano),
		Users:      users,
		Channels:   entries,
		Usergroups: groups,
		DMCount:    &dms,
	}, nil
}

var teamIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// DirectoryStore reads and writes the cached directory, one file per workspace.
type DirectoryStore struct {
	Root string
}

// NewDirectoryStore points at the archive root the directories live under.
func NewDirectoryStore(root string) *DirectoryStore {
	return &DirectoryStore{Root: root}
}

func (s *DirectoryStore) path(teamID string) (string, error) {
	if !teamIDPattern.MatchString(teamID) {
		return "", errors.New("Invalid team id: " + teamID)
	}

	return filepath.Join(s.Root, teamID, "directory.json"), nil
}

// Read answers the cached directory, or nil when there is none to answer with.
// A file that does not parse is the same as none: a directory is a cache.
func (s *DirectoryStore) Read(teamID string) (*Directory, error) {
	file, err := s.path(teamID)
	if err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(file) //nolint:gosec // the path is built from a validated team id
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}

		return nil, err
	}

	var directory Directory
	if err := json.Unmarshal(raw, &directory); err != nil {
		//nolint:nilerr // a directory is a cache: a file that does not parse is no cache
		return nil, nil
	}

	if directory.Version != 1 || directory.Users == nil {
		return nil, nil
	}

	directory.Channels = normalizeChannels(raw)

	return &directory, nil
}

// normalizeChannels folds the shape written before DMs were indexed
// (`{id, name}`) into the current one, so an old directory keeps working.
func normalizeChannels(raw []byte) []DirectoryChannel {
	var parsed struct {
		Channels []struct {
			ID         string `json:"id"`
			Kind       string `json:"kind"`
			Name       string `json:"name"`
			User       string `json:"user"`
			NumMembers int    `json:"num_members"`
		} `json:"channels"`
	}

	if err := json.Unmarshal(raw, &parsed); err != nil {
		return []DirectoryChannel{}
	}

	channels := make([]DirectoryChannel, 0, len(parsed.Channels))

	for _, entry := range parsed.Channels {
		if entry.ID == "" {
			continue
		}

		kind := KindChannel

		switch Kind(entry.Kind) {
		case KindChannel, KindPrivate, KindDM, KindMpIM:
			kind = Kind(entry.Kind)
		}

		channels = append(channels, DirectoryChannel{
			ID:         entry.ID,
			Kind:       kind,
			Name:       entry.Name,
			User:       entry.User,
			NumMembers: entry.NumMembers,
		})
	}

	return channels
}

// Write replaces the cached directory atomically, at 0700/0600: it holds names,
// not secrets, but it is the user's data either way.
func (s *DirectoryStore) Write(directory *Directory) (string, error) {
	file, err := s.path(directory.TeamID)
	if err != nil {
		return "", err
	}

	dir := filepath.Dir(file)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	encoded, err := json.Marshal(directory)
	if err != nil {
		return "", err
	}

	temporary := file + ".tmp"

	if err := os.WriteFile(temporary, append(encoded, '\n'), 0o600); err != nil {
		return "", err
	}

	if err := os.Rename(temporary, file); err != nil {
		return "", err
	}

	if err := platform.Harden(file); err != nil {
		return "", err
	}

	return file, nil
}

// LoadOptions is what a directory load is asked for.
type LoadOptions struct {
	Client DirectoryClient
	TeamID string
	Store  *DirectoryStore
	MaxAge time.Duration
	Force  bool
	Now    time.Time
}

// LoadDirectory answers the cached directory, refreshing it when it is missing,
// older than MaxAge, or written before DMs were indexed. A refresh that fails
// falls back to the stale copy, because an old name beats a raw id.
func LoadDirectory(options LoadOptions) (*Directory, error) {
	store := options.Store
	if store == nil {
		return nil, errors.New("LoadDirectory needs a store")
	}

	cached, err := store.Read(options.TeamID)
	if err != nil {
		return nil, err
	}

	maxAge := options.MaxAge
	if maxAge == 0 {
		maxAge = DirectoryMaxAge
	}

	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}

	if !options.Force && cached != nil {
		if savedAt, err := time.Parse(time.RFC3339Nano, cached.SavedAt); err == nil {
			fresh := now.Sub(savedAt) <= maxAge
			// DMCount says the directory knows DMs; without it, `dm:<name>` labels
			// would keep showing raw user ids for a day after an upgrade.
			if fresh && cached.DMCount != nil {
				return cached, nil
			}
		}
	}

	directory, err := FetchDirectory(options.Client, options.TeamID, now)
	if err != nil {
		//nolint:nilerr // an old name beats a raw id, so a failed refresh keeps the cache
		return cached, nil
	}

	if _, err := store.Write(directory); err != nil {
		//nolint:nilerr // the directory is already fetched and in hand; failing to cache it is not a failure
		return directory, nil
	}

	return directory, nil
}

// Memoized loads the directory at most once per call site, because naming a
// person is the only thing that needs it and most calls name a channel.
type Memoized struct {
	once      sync.Once
	load      func() (*Directory, error)
	directory *Directory
}

// NewMemoized takes the load that runs at most once.
func NewMemoized(load func() (*Directory, error)) *Memoized {
	memo := &Memoized{}

	memo.load = load

	return memo
}

// Get answers the directory, loading it the first time.
func (m *Memoized) Get() *Directory {
	m.once.Do(func() {
		directory, err := m.load()
		if err != nil {
			return
		}

		m.directory = directory
	})

	return m.directory
}

// Person answers a person reference with a user id.
func (m *Memoized) Person(reference string) (string, bool) {
	user := m.Get().UserByReference(reference)
	if user == nil {
		return "", false
	}

	return user.ID, true
}
