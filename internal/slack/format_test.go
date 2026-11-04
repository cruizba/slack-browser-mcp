package slack

import (
	"testing"
	"time"
)

// The zone is pinned so the assertion holds on a CI runner, which is set to UTC
// and would otherwise agree with a stray .UTC() in the implementation.
func TestFormatTSInZone(t *testing.T) {
	t.Parallel()

	const ts = "1792500000.000100"

	tests := []struct {
		name string
		zone *time.Location
		want string
	}{
		{name: "utc", zone: time.UTC, want: "2026-10-20 12:40:00"},
		{name: "madrid summer", zone: time.FixedZone("CEST", 2*60*60), want: "2026-10-20 14:40:00"},
		{name: "new york", zone: time.FixedZone("EDT", -4*60*60), want: "2026-10-20 08:40:00"},
		{name: "half hour offset", zone: time.FixedZone("IST", 5*60*60+30*60), want: "2026-10-20 18:10:00"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := formatTSIn(ts, test.zone); got != test.want {
				t.Fatalf("formatTSIn(%s, %s) = %q, want %q", ts, test.zone, got, test.want)
			}
		})
	}
}
