package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/config"
	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

func env(values map[string]string) platform.Env {
	return platform.Env{
		Getenv:    func(name string) string { return values[name] },
		LookupEnv: func(name string) (string, bool) { value, ok := values[name]; return value, ok },
		Home:      testHome,
		OS:        testOS,
	}
}

const (
	minute = time.Minute
	hour   = time.Hour
)

func TestFreshness_Defaults(t *testing.T) {
	t.Parallel()

	policy, err := config.Freshness(config.Settings{}, env(nil))
	if err != nil {
		t.Fatalf("Freshness: %v", err)
	}

	if policy.StaleAfter != 24*hour {
		t.Fatalf("stale = %v, want 24h", policy.StaleAfter)
	}

	if policy.RecentAfter != 15*minute {
		t.Fatalf("recent = %v, want 15m", policy.RecentAfter)
	}
}

func TestFreshness_FromSettings(t *testing.T) {
	t.Parallel()

	policy, err := config.Freshness(config.Settings{
		StaleAfterMinutes:  new(90.0),
		RecentAfterMinutes: new(2.0),
	}, env(nil))
	if err != nil {
		t.Fatalf("Freshness: %v", err)
	}

	if policy.StaleAfter != 90*minute || policy.RecentAfter != 2*minute {
		t.Fatalf("policy = %v/%v, want 90m/2m", policy.StaleAfter, policy.RecentAfter)
	}
}

func TestFreshness_EnvironmentWins(t *testing.T) {
	t.Parallel()

	policy, err := config.Freshness(
		config.Settings{StaleAfterMinutes: new(90.0), RecentAfterMinutes: new(2.0)},
		env(map[string]string{
			config.StaleAfterEnv:  "2880",
			config.RecentAfterEnv: " 30 ",
		}),
	)
	if err != nil {
		t.Fatalf("Freshness: %v", err)
	}

	if policy.StaleAfter != 48*hour {
		t.Fatalf("stale = %v, want 48h", policy.StaleAfter)
	}

	// Spreadsheet paste and hand-edited env files leave whitespace behind.
	if policy.RecentAfter != 30*minute {
		t.Fatalf("recent = %v, want 30m", policy.RecentAfter)
	}
}

func TestFreshness_RejectsUnusableValues(t *testing.T) {
	t.Parallel()

	t.Run("names the variable when the environment is wrong", func(t *testing.T) {
		t.Parallel()

		for _, value := range []string{"soon", "0", "-5"} {
			_, err := config.Freshness(config.Settings{}, env(map[string]string{config.StaleAfterEnv: value}))
			if err == nil {
				t.Fatalf("Freshness(%q) was accepted", value)
			}

			if !strings.Contains(err.Error(), config.StaleAfterEnv) || !strings.Contains(err.Error(), `"`+value+`"`) {
				t.Fatalf("error %q, want it to name the variable and the value", err)
			}
		}
	})

	t.Run("blames the settings file when the file is wrong", func(t *testing.T) {
		t.Parallel()

		_, err := config.Freshness(config.Settings{RecentAfterMinutes: new(0.0)}, env(nil))
		if err == nil || !strings.Contains(err.Error(), testFile) || !strings.Contains(err.Error(), "positive number of minutes") {
			t.Fatalf("error = %v, want it to blame %s", err, testFile)
		}

		// The value is quoted back the way the file wrote it, so a fractional one has
		// to come out fractional: the shortest-round-trip formatting is what a reader
		// uses to find the line, and "-1.5" that reads "got -1.500000" or "got -2" is
		// a worse error message than none at all.
		_, err = config.Freshness(config.Settings{RecentAfterMinutes: new(-1.5)}, env(nil))
		if err == nil || !strings.Contains(err.Error(), "got -1.5.") {
			t.Fatalf("error = %v, want it to quote the value as -1.5", err)
		}
	})
}

func TestWantsRefresh(t *testing.T) {
	t.Parallel()

	policy, err := config.Freshness(config.Settings{}, env(nil))
	if err != nil {
		t.Fatalf("Freshness: %v", err)
	}

	age := func(value time.Duration) *time.Duration { return &value }

	tests := []struct {
		name  string
		age   *time.Duration
		atNow bool
		want  bool
	}{
		{name: "nothing stored, a history read", age: nil, atNow: false, want: true},
		{name: "nothing stored, a read at now", age: nil, atNow: true, want: true},
		{name: "an hour old, a history read", age: age(hour), atNow: false, want: false},
		{name: "a day minus a minute, a history read", age: age(24*hour - minute), atNow: false, want: false},
		{name: "exactly a day, a history read", age: age(24 * hour), atNow: false, want: true},
		{name: "three days, a history read", age: age(72 * hour), atNow: false, want: true},
		{name: "five minutes, a read at now", age: age(5 * minute), atNow: true, want: false},
		{name: "fourteen minutes, a read at now", age: age(14 * minute), atNow: true, want: false},
		{name: "exactly fifteen minutes, a read at now", age: age(15 * minute), atNow: true, want: true},
		{name: "two hours, a read at now", age: age(2 * hour), atNow: true, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := config.WantsRefresh(tt.age, tt.atNow, policy); got != tt.want {
				t.Fatalf("WantsRefresh() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDescribePolicy(t *testing.T) {
	t.Parallel()

	policy, err := config.Freshness(config.Settings{}, env(nil))
	if err != nil {
		t.Fatalf("Freshness: %v", err)
	}

	text := config.DescribePolicy(policy)

	for _, want := range []string{"older than 24 h", "15 min for a read that reaches the newest messages"} {
		if !strings.Contains(text, want) {
			t.Fatalf("DescribePolicy() = %q, want it to contain %q", text, want)
		}
	}
}

// The boundaries are where a duration reads wrong to a person: "60 min" instead of
// "1 h", or "48 h" instead of "2 d". They are tested at the exact value because a
// mutant that flips `<` to `<=` changes nothing anywhere else.
func TestDescribePolicyAtTheBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		older string
		want  []string
	}{
		{name: "a minute under an hour", older: "59", want: []string{"59 min"}},
		{name: "exactly an hour", older: "60", want: []string{"1 h"}},
		{name: "an hour under two days", older: "2820", want: []string{"47 h"}},
		{name: "exactly two days", older: "2880", want: []string{"2 d"}},
		{name: "two days and a bit", older: "2881", want: []string{"2 d"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			policy, err := config.Freshness(config.Settings{}, env(map[string]string{
				config.StaleAfterEnv: test.older, config.RecentAfterEnv: "15",
			}))
			if err != nil {
				t.Fatalf("Freshness(%q): %v", test.older, err)
			}

			sentence := config.DescribePolicy(policy)

			for _, wanted := range test.want {
				if !strings.Contains(sentence, wanted) {
					t.Fatalf("DescribePolicy = %q, want it to say %q", sentence, wanted)
				}
			}
		})
	}
}
