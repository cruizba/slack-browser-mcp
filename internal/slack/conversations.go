package slack

import "strings"

// ConversationRef is a conversation named the way a person names it, before a
// label is derived from it.
type ConversationRef struct {
	ID   string
	Name string
	// Peer is the person on the other side of a DM, as a user id.
	Peer string
	Kind Kind
}

var prefixes = map[Kind]string{
	KindChannel: "#",
	KindPrivate: "private:",
	KindDM:      "dm:",
	KindMpIM:    "group:",
}

func inferKind(ref ConversationRef, entry *DirectoryChannel) Kind {
	if ref.Kind != "" {
		return ref.Kind
	}

	if entry != nil {
		return entry.Kind
	}

	// The archive of a DM records the peer's user id as the channel name, because
	// that is all Slack has to say about who the conversation is with.
	if ref.Peer != "" || userIDPattern.MatchString(ref.Name) || strings.HasPrefix(ref.ID, "D") {
		return KindDM
	}

	if groupDMName.MatchString(ref.Name) {
		return KindMpIM
	}

	return KindChannel
}

func directName(ref ConversationRef, entry *DirectoryChannel, directory *Directory) string {
	peer := ref.Peer

	if peer == "" && entry != nil {
		peer = entry.User
	}

	if peer == "" && userIDPattern.MatchString(ref.Name) {
		peer = ref.Name
	}

	if peer != "" {
		if name := directory.User(peer); name != "" {
			return name
		}

		return peer
	}

	if entry != nil && entry.Name != "" {
		return entry.Name
	}

	if ref.Name != "" {
		return ref.Name
	}

	return ref.ID
}

func groupName(ref ConversationRef, entry *DirectoryChannel, directory *Directory) string {
	name := ref.Name
	if name == "" && entry != nil {
		name = entry.Name
	}

	labels := make([]string, 0, 4)

	for _, handle := range groupDMHandles(name) {
		user := directory.UserByHandle(handle)
		if user == nil {
			labels = append(labels, handle)

			continue
		}

		if label := directory.User(user.ID); label != "" {
			labels = append(labels, label)
		} else {
			labels = append(labels, handle)
		}
	}

	if len(labels) > 0 {
		return strings.Join(labels, ", ")
	}

	if name != "" {
		return name
	}

	return ref.ID
}

func conversationName(ref ConversationRef, kind Kind, directory *Directory) string {
	entry := directory.Channel(ref.ID)

	switch kind {
	case KindDM:
		return directName(ref, entry, directory)
	case KindMpIM:
		return groupName(ref, entry, directory)
	case KindChannel, KindPrivate:
		if entry != nil && entry.Name != "" {
			return entry.Name
		}

		if ref.Name != "" {
			return ref.Name
		}

		return ref.ID
	default:
		if entry != nil && entry.Name != "" {
			return entry.Name
		}

		if ref.Name != "" {
			return ref.Name
		}

		return ref.ID
	}
}

// ConversationLabel is the one-line label an agent reads in place of an id:
// `#random`, `private:leads`, `dm:Ana Ruiz`, `group:Ana, Michel`.
func ConversationLabel(ref ConversationRef, directory *Directory) string {
	kind := inferKind(ref, directory.Channel(ref.ID))

	return prefixes[kind] + conversationName(ref, kind, directory)
}

// ChannelLabel is the label of a channel Slack described.
func ChannelLabel(channel Channel, directory *Directory) string {
	described := channel.IsIM || channel.IsMpIM || channel.IsPrivate

	ref := ConversationRef{ID: channel.ID, Name: channel.Name, Peer: channel.User}
	if described {
		ref.Kind = ConversationKind(channel)
	}

	return ConversationLabel(ref, directory)
}

func asChannel(entry DirectoryChannel) Channel {
	return Channel{
		ID:         entry.ID,
		Name:       entry.Name,
		User:       entry.User,
		NumMembers: entry.NumMembers,
		IsPrivate:  entry.Kind == KindPrivate,
		IsIM:       entry.Kind == KindDM,
		IsMpIM:     entry.Kind == KindMpIM,
	}
}

// DirectoryConversation answers a reference from the cached directory, so naming
// a channel does not cost a `users.conversations` call. Ok is false when the
// directory cannot decide, and the caller has to ask Slack.
func DirectoryConversation(directory *Directory, reference string) (Channel, bool) {
	if directory == nil {
		return Channel{}, false
	}

	printed := ParsePrintedReference(reference)

	if printed.ID != "" {
		if entry := directory.Channel(printed.ID); entry != nil {
			return asChannel(*entry), true
		}

		return Channel{ID: printed.ID}, true
	}

	raw := strings.TrimPrefix(printed.Reference, "#")
	if raw == "" {
		return Channel{}, false
	}

	if entry := directory.Channel(raw); entry != nil {
		return asChannel(*entry), true
	}

	if conversationIDPattern.MatchString(raw) {
		return Channel{ID: raw}, true
	}

	if entry, ok := matchDirectoryName(directory, raw); ok {
		return asChannel(entry), true
	}

	person := directory.UserByReference(stripAddressing(raw))
	if person == nil {
		return Channel{}, false
	}

	for _, entry := range directory.Channels {
		if entry.Kind == KindDM && entry.User == person.ID {
			return asChannel(entry), true
		}
	}

	return Channel{}, false
}

// matchDirectoryName answers exactly one name, and refuses to guess when several
// entries match.
func matchDirectoryName(directory *Directory, raw string) (DirectoryChannel, bool) {
	lowered := strings.ToLower(raw)

	var exact, partial []DirectoryChannel

	for _, entry := range directory.Channels {
		if entry.Name == "" {
			continue
		}

		name := strings.ToLower(entry.Name)
		if name == lowered {
			exact = append(exact, entry)
		}

		if strings.Contains(name, lowered) {
			partial = append(partial, entry)
		}
	}

	switch {
	case len(exact) == 1:
		return exact[0], true
	case len(exact) > 1:
		return DirectoryChannel{}, false
	case len(partial) == 1:
		return partial[0], true
	default:
		return DirectoryChannel{}, false
	}
}

// ConversationFilter is what a reference means to the archive index. A DM is
// stored under the peer's user id, so naming the person resolves to that id.
type ConversationFilter struct {
	Channel     string
	ChannelName string
}

// FilterOf turns a reference into what the index can filter on. Several
// directories are accepted for a search that spans workspaces: a person exists in
// one of them, and ids are unique across workspaces.
func FilterOf(reference string, directories []*Directory) ConversationFilter {
	printed := ParsePrintedReference(reference)

	if printed.ID != "" {
		return ConversationFilter{Channel: printed.ID}
	}

	raw := printed.Reference
	if conversationIDPattern.MatchString(raw) {
		return ConversationFilter{Channel: raw}
	}

	name := stripAddressing(raw)
	if name == "" {
		return ConversationFilter{}
	}

	addressingPerson := strings.HasPrefix(raw, "dm:") || strings.HasPrefix(raw, "@")

	if id, ok := channelNamed(directories, name, addressingPerson); ok {
		return ConversationFilter{Channel: id}
	}

	for _, directory := range directories {
		person := directory.UserByReference(name)
		if person == nil {
			continue
		}

		if id, ok := directWith(directory, person.ID); ok {
			return ConversationFilter{Channel: id}
		}

		return ConversationFilter{ChannelName: person.ID}
	}

	return ConversationFilter{ChannelName: name}
}

// channelNamed answers a channel whose name is an exact match, which is what a
// search filters on. A person reference skips this: `dm:ana` names the person,
// not a channel that happens to be called ana.
func channelNamed(directories []*Directory, name string, addressingPerson bool) (string, bool) {
	if addressingPerson {
		return "", false
	}

	for _, directory := range directories {
		if directory == nil {
			continue
		}

		for _, entry := range directory.Channels {
			if entry.Name != "" && strings.EqualFold(entry.Name, name) {
				return entry.ID, true
			}
		}
	}

	return "", false
}

func directWith(directory *Directory, userID string) (string, bool) {
	if directory == nil {
		return "", false
	}

	for _, entry := range directory.Channels {
		if entry.Kind == KindDM && entry.User == userID {
			return entry.ID, true
		}
	}

	return "", false
}

func stripAddressing(raw string) string {
	name := strings.TrimPrefix(raw, "#")

	for _, prefix := range []string{"dm:", "group:", "private:", "@"} {
		if trimmed, ok := strings.CutPrefix(name, prefix); ok {
			name = trimmed

			break
		}
	}

	return name
}
