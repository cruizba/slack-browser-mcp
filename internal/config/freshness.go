package config

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

// Two thresholds, because two reads ask different questions of the same archive.
// A read that ends at "now" is about what just happened, and a day-old tail would
// answer the wrong question, so it tolerates minutes. A read that pages back
// through history only wants the archive from the same day: an older tail cannot
// change what happened last week.
const (
	DefaultStaleAfterMinutes  = 24 * 60
	DefaultRecentAfterMinutes = 15

	StaleAfterEnv  = "SLACK_BROWSER_MCP_STALE_AFTER_MINUTES"
	RecentAfterEnv = "SLACK_BROWSER_MCP_RECENT_AFTER_MINUTES"
)

// FreshnessPolicy is how old the archive may get before a read refreshes it in
// the background.
type FreshnessPolicy struct {
	StaleAfter  time.Duration
	RecentAfter time.Duration
}

// Freshness resolves the policy from the environment over the settings file over
// the defaults, so a single shell can be tuned without editing anything shared.
func Freshness(settings Settings, env platform.Env) (FreshnessPolicy, error) {
	stale, err := threshold(StaleAfterEnv, settings.StaleAfterMinutes, DefaultStaleAfterMinutes, env)
	if err != nil {
		return FreshnessPolicy{}, err
	}

	recent, err := threshold(RecentAfterEnv, settings.RecentAfterMinutes, DefaultRecentAfterMinutes, env)
	if err != nil {
		return FreshnessPolicy{}, err
	}

	return FreshnessPolicy{StaleAfter: stale, RecentAfter: recent}, nil
}

// WantsRefresh reports whether a read should start a background sync. A nil age
// means nothing is stored for this workspace yet, so whatever the read finds is a
// miss and fetching it cannot make the answer worse. atNow is true for a read
// whose window is open at the top: one that expects the newest messages.
func WantsRefresh(age *time.Duration, atNow bool, policy FreshnessPolicy) bool {
	if age == nil {
		return true
	}

	limit := policy.StaleAfter
	if atNow {
		limit = policy.RecentAfter
	}

	return *age >= limit
}

// DescribePolicy reads as a sentence an agent can forward to the user.
func DescribePolicy(policy FreshnessPolicy) string {
	return "refresh: background sync when the archive is older than " + humanize(policy.StaleAfter) +
		", or " + humanize(policy.RecentAfter) + " for a read that reaches the newest messages"
}

func threshold(name string, configured *float64, fallback float64, env platform.Env) (time.Duration, error) {
	raw := ""
	if env.Getenv != nil {
		raw = strings.TrimSpace(env.Getenv(name))
	}

	if raw != "" {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || value <= 0 {
			return 0, &InvalidError{
				Message: name + " must be a positive number of minutes, got " + quote(raw) + ".",
			}
		}

		return minutes(value), nil
	}

	if configured != nil {
		if *configured <= 0 {
			return 0, &InvalidError{
				Message: `"` + name + ` in config.json" must be a positive number of minutes, got ` +
					strconv.FormatFloat(*configured, 'f', -1, 64) + ".",
			}
		}

		return minutes(*configured), nil
	}

	return minutes(fallback), nil
}

func minutes(value float64) time.Duration {
	return time.Duration(value * float64(time.Minute))
}

func quote(value string) string {
	return `"` + value + `"`
}

func humanize(window time.Duration) string {
	total := window.Minutes()

	if total < 60 {
		return formatNumber(total) + " min"
	}

	if hours := total / 60; hours < 48 {
		return formatNumber(hours) + " h"
	}

	// Whole days, because a day is 1440 minutes and anything that does not divide
	// evenly would otherwise print its float noise: 2881 minutes is "2 d", not
	// "2.0006944444444446 d". The TypeScript had the longer form; prose that a person
	// reads is worth diverging over.
	return formatNumber(math.Floor(total/60/24)) + " d"
}

func formatNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
