package forum

import (
	"sort"
	"strconv"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/archive"
)

// Peer is one agent in a channel, as the announcements in the archive describe it.
//
// It carries two separate answers because they come from two different senders: a
// beacon says the machine is up (the poller may post it, so a daemon keeps a peer
// from rotting without speaking for its user), and a presence says what the person in
// front of a session is doing. Reading them as one number is what let a heartbeat
// claim "listening" for a day after everybody had gone home.
type Peer struct {
	From         string
	Name         string
	Host         string
	Capabilities []string
	Sent         string
	// TS is the newest message from this peer of any kind: what machine liveness is
	// measured from.
	TS string
	// Owner is the Slack user that posted the announcement: the person this agent
	// works for. It comes from the archived message, not from the envelope.
	Owner string
	// InteractiveTS is the newest message that is not a beacon. A daemon cannot make
	// a peer look busy.
	InteractiveTS string
	// State is what the last presence message claimed about the person, empty when it
	// claimed nothing.
	State string
	Note  string
}

func claimOf(envelope *Envelope) (state, note string, claimed bool) {
	if envelope.Kind != KindPresence {
		return "", "", false
	}

	if envelope.State != "here" && envelope.State != "away" {
		return "", "", false
	}

	return envelope.State, envelope.Note, true
}

// RosterFrom derives the roster from the announcements in the archive, one entry per
// `name@host`, the latest winning. The archive hands messages back oldest first.
func RosterFrom(messages []archive.ArchivedMessage, ignored []string) []Peer {
	retired := make(map[string]bool, len(ignored))
	for _, from := range ignored {
		retired[from] = true
	}

	byFrom := map[string]*Peer{}
	order := make([]string, 0, len(messages))

	for _, message := range messages {
		if message.DeletedAt != "" {
			continue
		}

		envelope := EnvelopeOf(CarrierOf(message))
		if envelope == nil || retired[envelope.From] {
			continue
		}

		if envelope.Kind == KindPresence && envelope.State == "gone" {
			delete(byFrom, envelope.From)

			continue
		}

		previous := byFrom[envelope.From]

		if !isAnnouncement(envelope) {
			// A task or a reply proves the peer was there, and nothing else.
			if previous != nil {
				previous.TS = message.TS
				previous.InteractiveTS = message.TS
			}

			continue
		}

		if previous == nil {
			order = append(order, envelope.From)
		}

		byFrom[envelope.From] = mergeAnnouncement(previous, envelope, message)
	}

	roster := make([]Peer, 0, len(byFrom))

	for _, from := range order {
		if peer, ok := byFrom[from]; ok {
			roster = append(roster, *peer)
		}
	}

	sort.SliceStable(roster, func(left, right int) bool {
		return roster[left].From < roster[right].From
	})

	return roster
}

func isAnnouncement(envelope *Envelope) bool {
	switch envelope.Kind {
	case KindRegister, KindBeacon, KindPresence:
		return true
	default:
		return false
	}
}

// mergeAnnouncement folds one announcement over what the roster already knew.
func mergeAnnouncement(previous *Peer, envelope *Envelope, message archive.ArchivedMessage) *Peer {
	peer := &Peer{
		From: envelope.From,
		Name: envelope.Name,
		Host: envelope.Host,
		Sent: envelope.Sent,
		TS:   message.TS,
	}

	if previous != nil {
		*peer = *previous
		peer.TS = message.TS
	}

	if envelope.Kind == KindRegister {
		peer.Capabilities = envelope.Capabilities
	}

	// A presence message always speaks for the person, even when the answer is "no
	// claim" (the old listening/idle shape); a register or a beacon leaves what the
	// person last said alone.
	switch envelope.Kind {
	case KindPresence:
		state, note, claimed := claimOf(envelope)
		peer.State = state
		peer.Note = note

		if claimed {
			peer.InteractiveTS = message.TS
		}
	case KindRegister:
		// Only what a session writes counts as somebody being there; a beacon is the
		// poller, and must not make a peer look busy.
		peer.InteractiveTS = message.TS
	case KindBeacon:
	}

	if message.User != "" {
		peer.Owner = message.User
	}

	return peer
}

// PeerFor answers the roster entry of one label.
func PeerFor(roster []Peer, from string) (Peer, bool) {
	for _, peer := range roster {
		if peer.From == from {
			return peer, true
		}
	}

	return Peer{}, false
}

// HostsOf is every host a name is known at, which is what turns an ambiguous name
// into a sentence the user can act on.
func HostsOf(roster []Peer, name string) []string {
	hosts := make([]string, 0, 2)

	for _, peer := range roster {
		if peer.Name == name {
			hosts = append(hosts, peer.Host)
		}
	}

	return hosts
}

// MachineStatus says whether the machine is there to answer at all. Silence is the
// only way out, so nothing has to be posted to make it true.
type MachineStatus string

// HumanStatus says whether a person is in front of it now.
type HumanStatus string

// The two answers a peer gives at once: whether the machine is up, and whether a
// person is in front of it.
const (
	MachineUp  MachineStatus = "up"
	MachineOut MachineStatus = "out"

	HumanHere    HumanStatus = "here"
	HumanAway    HumanStatus = "away"
	HumanUnknown HumanStatus = "unknown"
)

func ageOf(ts string, now time.Time) time.Duration {
	seconds, err := strconv.ParseFloat(ts, 64)
	if err != nil {
		return 0
	}

	return now.Sub(time.UnixMilli(int64(seconds * 1000)))
}

// MachineStatusOf measures from the newest message of any kind.
func MachineStatusOf(peer Peer, ttlHours float64, now time.Time) MachineStatus {
	if ageOf(peer.TS, now) > time.Duration(ttlHours*float64(time.Hour)) {
		return MachineOut
	}

	return MachineUp
}

// HumanStatusOf measures from the newest message a session wrote: `here` is a claim
// with a short life, refreshed for free by any message, and `away` stands until
// something replaces it.
func HumanStatusOf(peer Peer, hereSeconds int, ttlHours float64, now time.Time) HumanStatus {
	if MachineStatusOf(peer, ttlHours, now) == MachineOut {
		return HumanUnknown
	}

	if peer.State == "away" {
		return HumanAway
	}

	if peer.InteractiveTS == "" {
		return HumanUnknown
	}

	if ageOf(peer.InteractiveTS, now) < time.Duration(hereSeconds)*time.Second {
		return HumanHere
	}

	return HumanUnknown
}
