package forum

import (
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

// Who an agent is on the forum: a name, a host, and the kinds of work it takes.
// The identity is per process, not per machine: two agents on one laptop share
// `forum.json`, so a single name would make 1:1 addressing collapse into a queue.
const (
	// AgentEnv names this process, ahead of the MCP client name and the file.
	AgentEnv = "SLACK_BROWSER_MCP_AGENT"
	// MaxCapabilities is how many kinds of work an agent may announce.
	MaxCapabilities = 8
)

var (
	nameStripper = regexp.MustCompile(`[^a-z0-9._-]+`)
	nameRuns     = regexp.MustCompile(`-{2,}`)
	nameEdges    = regexp.MustCompile(`^[-.]+|[-.]+$`)
	trailingEdge = regexp.MustCompile(`[-.]+$`)
	spaceRuns    = regexp.MustCompile(`\s+`)
)

// Identity is this process on the forum.
type Identity struct {
	Name         string
	Host         string
	Capabilities []string
}

// NormalizeName keeps what survives a copy-paste: lowercase, no spaces, no `@` (it
// is the separator), no line break.
func NormalizeName(raw string) string {
	flat := nameStripper.ReplaceAllString(strings.ToLower(raw), "-")
	flat = nameRuns.ReplaceAllString(flat, "-")
	flat = nameEdges.ReplaceAllString(flat, "")

	if len(flat) > MaxNameChars {
		flat = flat[:MaxNameChars]
	}

	return trailingEdge.ReplaceAllString(flat, "")
}

// DefaultHost is this machine's short name.
func DefaultHost() string {
	host, err := os.Hostname()
	if err != nil {
		return "localhost"
	}

	short, _, _ := strings.Cut(host, ".")
	if name := NormalizeName(short); name != "" {
		return name
	}

	return "localhost"
}

// LabelOf is how a peer is addressed.
func LabelOf(name, host string) string {
	return name + "@" + host
}

// SameCapabilities is set equality: a re-register that only reorders the tags is not
// a change, and nothing is announced twice.
func SameCapabilities(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}

	set := make(map[string]bool, len(right))
	for _, tag := range right {
		set[tag] = true
	}

	for _, tag := range left {
		if !set[tag] {
			return false
		}
	}

	return true
}

// AgentName answers this process's name: an explicit environment variable first,
// because it is scoped to one client registration; the MCP client name is the
// zero-config default, and a plain shell falls back to the file.
func AgentName(env platform.Env, client, fallback string) string {
	if fromEnv, present := platform.Lookup(env, AgentEnv); present && fromEnv != "" {
		if name := NormalizeName(fromEnv); name != "" {
			return name
		}
	}

	if client != "" {
		if name := NormalizeName(client); name != "" {
			return name
		}
	}

	if name := NormalizeName(fallback); name != "" {
		return name
	}

	return "agent"
}

// ParseCapability folds a capability to the one word every surface prints: `Review`,
// `review ` and `review` are one thing to a peer reading the roster.
func ParseCapability(raw string) (string, error) {
	tag := strings.ToLower(strings.TrimSpace(raw))
	tag = spaceRuns.ReplaceAllString(tag, "-")

	if tag == "" {
		return "", errors.New("a capability cannot be empty")
	}

	if len(tag) > MaxTagChars {
		return "", errors.New("\"" + tag + "\" is longer than " + strconv.Itoa(MaxTagChars) +
			" characters; capabilities are short words")
	}

	return tag, nil
}

// ParseCapabilities reads a comma-separated list, refusing duplicates twice over.
func ParseCapabilities(raw string) ([]string, error) {
	seen := map[string]bool{}
	tags := make([]string, 0, 4)

	for entry := range strings.SplitSeq(raw, ",") {
		if strings.TrimSpace(entry) == "" {
			continue
		}

		tag, err := ParseCapability(entry)
		if err != nil {
			return nil, err
		}

		if seen[tag] {
			continue
		}

		seen[tag] = true
		tags = append(tags, tag)
	}

	if len(tags) > MaxCapabilities {
		return nil, errors.New("at most 8 capabilities; narrow the list")
	}

	return tags, nil
}
