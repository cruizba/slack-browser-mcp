package archive

import (
	"sort"
	"strconv"
	"time"
)

// TeamStatus is how fresh one workspace's archive is, so a read tool can say how
// old what it returns is instead of implying it is live.
type TeamStatus struct {
	TeamID         string
	LastRun        string
	AgeMS          *int64
	Channels       int
	PendingThreads int
	Errors         []ChannelError
}

// ChannelError is the last failure of one channel, kept so a sync that half
// worked does not look like one that did.
type ChannelError struct {
	Channel string
	Error   string
}

func latestRun(state SyncState, teamID string) string {
	team := state.Teams[teamID]
	if team == nil {
		return ""
	}

	if team.LastRun != "" {
		return team.LastRun
	}

	runs := make([]string, 0, len(team.Channels))

	for _, channel := range team.Channels {
		if channel.LastSync != "" {
			runs = append(runs, channel.LastSync)
		}
	}

	sort.Strings(runs)

	if len(runs) == 0 {
		return ""
	}

	return runs[len(runs)-1]
}

// TeamStatusOf reports how old one workspace's archive is.
func TeamStatusOf(state SyncState, teamID string, now time.Time) TeamStatus {
	team := state.Teams[teamID]
	lastRun := latestRun(state, teamID)

	status := TeamStatus{TeamID: teamID, LastRun: lastRun}

	if lastRun != "" {
		if when, err := time.Parse(time.RFC3339Nano, lastRun); err == nil {
			age := max(int64(0), now.Sub(when).Milliseconds())
			status.AgeMS = &age
		}
	}

	if team == nil {
		return status
	}

	status.Channels = len(team.Channels)

	ids := make([]string, 0, len(team.Channels))
	for id := range team.Channels {
		ids = append(ids, id)
	}

	sort.Strings(ids)

	for _, id := range ids {
		channel := team.Channels[id]
		status.PendingThreads += len(channel.PendingThread)

		if channel.LastError != "" && len(status.Errors) < 10 {
			status.Errors = append(status.Errors, ChannelError{Channel: id, Error: channel.LastError})
		}
	}

	return status
}

// OldestAge is the age of the least recently synced workspace, which is the one
// a reader has to be warned about.
func OldestAge(state SyncState, now time.Time) *int64 {
	var oldest *int64

	for teamID := range state.Teams {
		status := TeamStatusOf(state, teamID, now)
		if status.AgeMS == nil {
			continue
		}

		if oldest == nil || *status.AgeMS > *oldest {
			oldest = status.AgeMS
		}
	}

	return oldest
}

// FormatAge is how old the archive is, in the words a reader expects.
func FormatAge(ageMS *int64) string {
	if ageMS == nil {
		return "never synced"
	}

	minutes := *ageMS / 60_000

	switch {
	case minutes < 1:
		return "just now"
	case minutes < 60:
		return strconv.FormatInt(minutes, 10) + " min ago"
	}

	hours := minutes / 60
	if hours < 48 {
		return strconv.FormatInt(hours, 10) + " h ago"
	}

	return strconv.FormatInt(hours/24, 10) + " d ago"
}
