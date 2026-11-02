package forum

import (
	"math"
	"strconv"
)

// Limits are the clocks that keep two agents from talking past each other: how long
// a task lives, how long silence counts as stalled, how often a peer says it is
// there, and how long a peer may be quiet before it reads as out. They live in
// `forum.json` so a user can tune them without a code change.
type Limits struct {
	TTLHours        float64 `json:"ttl_hours"`
	AckSeconds      int     `json:"ack_seconds"`
	StallSeconds    int     `json:"stall_seconds"`
	HereSeconds     int     `json:"here_seconds"`
	PresenceSeconds int     `json:"presence_seconds"`
	PeerTTLHours    int     `json:"peer_ttl_hours"`
}

// DefaultLimits is what a channel gets when the file says nothing.
var DefaultLimits = Limits{
	TTLHours:        24,
	AckSeconds:      900,
	StallSeconds:    1800,
	HereSeconds:     3600,
	PresenceSeconds: 86400,
	PeerTTLHours:    72,
}

// LimitsWith folds a partial file over the defaults: anything absent keeps the
// default, which is what lets the file carry one number without carrying all six.
func LimitsWith(partial *Limits) Limits {
	if partial == nil {
		return DefaultLimits
	}

	limits := DefaultLimits

	if partial.TTLHours != 0 {
		limits.TTLHours = partial.TTLHours
	}

	if partial.AckSeconds != 0 {
		limits.AckSeconds = partial.AckSeconds
	}

	if partial.StallSeconds != 0 {
		limits.StallSeconds = partial.StallSeconds
	}

	if partial.HereSeconds != 0 {
		limits.HereSeconds = partial.HereSeconds
	}

	if partial.PresenceSeconds != 0 {
		limits.PresenceSeconds = partial.PresenceSeconds
	}

	if partial.PeerTTLHours != 0 {
		limits.PeerTTLHours = partial.PeerTTLHours
	}

	return limits
}

// Timings are the same clocks in milliseconds, which is what a comparison uses.
func Timings(limits Limits) (ackMS, stallMS, ttlMS int64) {
	return int64(limits.AckSeconds) * 1000,
		int64(limits.StallSeconds) * 1000,
		int64(limits.TTLHours * 3_600_000)
}

// RefreshWindowDays is how far back a forum refresh re-reads the channel. Replies
// never show up in `conversations.history`, so a parent older than the cursor is
// only seen again if the window reaches it; it has to cover a task's whole life.
func RefreshWindowDays(limits Limits) int {
	return max(1, int(math.Ceil(limits.TTLHours/24)))
}

func humanDuration(seconds int) string {
	if seconds < 3600 {
		return strconv.Itoa((seconds+30)/60) + "m"
	}

	hours := float64(seconds) / 3600

	if hours == math.Trunc(hours) {
		return strconv.FormatFloat(hours, 'f', 0, 64) + "h"
	}

	return strconv.FormatFloat(hours, 'f', 1, 64) + "h"
}

// DescribeLimits is one line both surfaces print, so the numbers cannot drift apart.
func DescribeLimits(limits Limits) string {
	return "ttl " + trimNumber(limits.TTLHours) + "h · ack " + humanDuration(limits.AckSeconds) +
		" · stall " + humanDuration(limits.StallSeconds) + " · here " + humanDuration(limits.HereSeconds) +
		" · beacon " + humanDuration(limits.PresenceSeconds)
}

func trimNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
