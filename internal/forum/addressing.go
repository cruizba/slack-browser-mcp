package forum

import (
	"strconv"
	"strings"

	"github.com/cruizba/slack-browser-mcp/internal/failure"
)

// Target is what a `to` argument means: one agent, one name that must be unique, or
// the whole channel. It is one string on the wire but three intended shapes, so the
// rules live here rather than at posting time:
// an ambiguous name is refused with the candidates instead of guessed, because
// sending work to the wrong agent looks exactly like sending it to the right one.
type Target struct {
	// Anyone is the broadcast: work for whoever takes it.
	Anyone bool
	Name   string
	Host   string
}

// ParseTarget reads what a person typed as a destination.
func ParseTarget(raw string) (Target, error) {
	value := strings.TrimSpace(raw)

	if value == "" {
		return Target{}, &failure.ForumError{
			Message: "A task needs a destination.",
			Hint:    "Use `name@host`, a unique `name`, or `*` for everyone in the channel.",
		}
	}

	if value == "*" {
		return Target{Anyone: true}, nil
	}

	parts := strings.Split(value, "@")
	if len(parts) > 2 {
		return Target{}, &failure.ForumError{
			Message: "\"" + raw + "\" is not a destination.",
			Hint:    "A peer is `name` or `name@host`.",
		}
	}

	name := NormalizeName(parts[0])
	if name == "" {
		return Target{}, &failure.ForumError{
			Message: "\"" + raw + "\" has no usable name.",
			Hint:    "Names are letters, digits, dot, dash.",
		}
	}

	if len(parts) == 1 {
		return Target{Name: name}, nil
	}

	host := NormalizeName(parts[1])
	if host == "" {
		return Target{}, &failure.ForumError{
			Message: "\"" + raw + "\" ends with an empty host.",
			Hint:    "Drop the @ or name the host.",
		}
	}

	return Target{Name: name, Host: host}, nil
}

// TargetLabel is how the destination prints.
func TargetLabel(target Target) string {
	if target.Anyone {
		return "*"
	}

	if target.Host != "" {
		return target.Name + "@" + target.Host
	}

	return target.Name
}

// Candidates is every peer a target could mean.
func Candidates(roster []Peer, target Target) []Peer {
	if target.Anyone {
		return append([]Peer{}, roster...)
	}

	matching := make([]Peer, 0, 2)

	for _, peer := range roster {
		if peer.Name != target.Name {
			continue
		}

		if target.Host == "" || peer.Host == target.Host {
			matching = append(matching, peer)
		}
	}

	return matching
}

// ResolvePeer answers the one peer a target names, or explains why it cannot.
func ResolvePeer(roster []Peer, target Target) (Peer, error) {
	if target.Anyone {
		return Peer{}, &failure.ForumError{Message: "`*` does not name a single peer."}
	}

	matching := Candidates(roster, target)

	switch len(matching) {
	case 1:
		return matching[0], nil
	case 0:
		near := HostsOf(roster, target.Name)

		hint := "Run `agent-forum peers`, or `agent-forum register <name>` if that agent is you."
		if len(near) > 0 {
			hint = "\"" + target.Name + "\" is known at: " + strings.Join(near, ", ") + "."
		}

		return Peer{}, &failure.ForumError{
			Message: "No peer \"" + TargetLabel(target) + "\" in this channel's roster.",
			Hint:    hint,
		}
	default:
		froms := make([]string, 0, len(matching))
		for _, peer := range matching {
			froms = append(froms, peer.From)
		}

		return Peer{}, &failure.ForumError{
			Message: "\"" + target.Name + "\" matches " + strconv.Itoa(len(matching)) + " agents: " +
				strings.Join(froms, ", ") + ".",
			Hint: "Address one of them as `name@host`.",
		}
	}
}
