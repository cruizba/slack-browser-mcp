package forum

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

// The two local marks behind the two liveness signals.
//
// A beacon says the machine is up and belongs to the poller, so it is throttled by a
// timestamp: one post per window. A claim says what the person in front of the session
// is doing, never comes from the poller, and only has to survive the archive lag
// between posting it and reading it back.
//
// They are separate files because they answer different questions. Reading the archive
// instead is not an option: a message that was just posted is not in it until the next
// sync, so an archive-only check would repost on every tool call. Each one is per
// identity, because two agents share a machine.
const (
	// PresenceEnv turns every announcement off for a machine that must stay read-only.
	PresenceEnv = "SLACK_BROWSER_MCP_NO_PRESENCE"

	claimState = "state"
)

var markKeyPattern = regexp.MustCompile(`[^A-Za-z0-9._@-]+`)

// BeaconThrottleMS is how often a peer re-announces that its machine is up.
func BeaconThrottleMS(presenceSeconds int) int64 {
	return int64(presenceSeconds) * 1000
}

func markKey(label string) string {
	return markKeyPattern.ReplaceAllString(label, "_")
}

// BeaconMarkPath is the file that remembers the last beacon for one identity.
func BeaconMarkPath(root, label string) string {
	return filepath.Join(root, ".beacon-"+markKey(label))
}

// ClaimMarkPath is the file that remembers the last claim for one identity.
func ClaimMarkPath(root, label string) string {
	return filepath.Join(root, ".claim-"+markKey(label))
}

func writeMark(file, contents string) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}

	if err := os.WriteFile(file, []byte(contents), 0o600); err != nil {
		return err
	}

	return platform.Harden(file)
}

// MarkBeacon records that this machine announced itself.
func MarkBeacon(root, label string, when time.Time) error {
	return writeMark(BeaconMarkPath(root, label), when.UTC().Format("2006-01-02T15:04:05.000Z"))
}

// LastBeacon answers when this identity last announced itself, in milliseconds.
func LastBeacon(root, label string) (int64, bool) {
	raw, err := os.ReadFile(BeaconMarkPath(root, label))
	if err != nil {
		return 0, false
	}

	stamp, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(raw)))
	if err != nil {
		return 0, false
	}

	return stamp.UnixMilli(), true
}

// MarkClaim records what the person in this session is doing.
func MarkClaim(root, label, state string, when time.Time) error {
	encoded, err := json.Marshal(map[string]any{claimState: state, "at": when.UnixMilli()})
	if err != nil {
		return err
	}

	return writeMark(ClaimMarkPath(root, label), string(encoded))
}

// Claim is what a session last claimed about the person in it.
type Claim struct {
	State string
	At    int64
}

// LastClaim answers the last claim for an identity.
func LastClaim(root, label string) (Claim, bool) {
	raw, err := os.ReadFile(ClaimMarkPath(root, label))
	if err != nil {
		return Claim{}, false
	}

	var parsed struct {
		State string  `json:"state"`
		At    float64 `json:"at"`
	}

	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Claim{}, false
	}

	if parsed.State != string(HumanHere) && parsed.State != string(HumanAway) {
		return Claim{}, false
	}

	return Claim{State: parsed.State, At: int64(parsed.At)}, true
}

// PresenceDisabled reports whether this machine must never announce itself.
func PresenceDisabled(env platform.Env) bool {
	value, present := platform.Lookup(env, PresenceEnv)

	return present && strings.TrimSpace(value) == "1"
}

// FormatMillis is the number of milliseconds as text, which a mark file needs.
func FormatMillis(value int64) string {
	return strconv.FormatInt(value, 10)
}
