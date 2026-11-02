package forum

import (
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/archive"
)

// CatchUpArgs is what a refresh of the bound channels needs. The forum reuses the
// archive's sync instead of its own fetcher: one write path means one lock, one set
// of tombstones, one place where a deletion is marked.
type CatchUpArgs struct {
	Bindings []Binding
	// Sync is the archive's sync, supplied by the caller because only it knows how to
	// build a client and an index for a workspace.
	Sync func(teamID string, channels []string, options archive.SyncOptions) (archive.SyncReport, error)
	// Deadline is one budget for the whole refresh, not one per workspace.
	Deadline time.Duration
	// RefreshDays re-reads this window so replies on older parents are noticed; see
	// [RefreshWindowDays].
	RefreshDays int
	Now         time.Time
	OnProgress  func(string)
}

// CatchUpReport is what a refresh found.
type CatchUpReport struct {
	Synced int
	Locked bool
	Errors []string
}

// maxThreads is how many thread parents one forum refresh drains: a coordination
// channel has few threads and they are the whole point.
const maxThreads = 25

// CatchUp refreshes the bound channels, one workspace at a time.
func CatchUp(args CatchUpArgs) CatchUpReport {
	report := CatchUpReport{}

	byTeam := map[string][]string{}
	order := make([]string, 0, 2)

	for _, binding := range args.Bindings {
		if _, seen := byTeam[binding.Workspace]; !seen {
			order = append(order, binding.Workspace)
		}

		byTeam[binding.Workspace] = append(byTeam[binding.Workspace], binding.Channel)
	}

	deadline := time.Now().Add(args.Deadline)
	if args.Deadline == 0 {
		deadline = time.Now().Add(25 * time.Second)
	}

	for _, teamID := range order {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			report.Errors = append(report.Errors, "deadline reached before every channel was refreshed")

			break
		}

		result, err := args.Sync(teamID, byTeam[teamID], archive.SyncOptions{
			Channels:    byTeam[teamID],
			Deadline:    remaining,
			MaxThreads:  maxThreads,
			RefreshDays: args.RefreshDays,
			Now:         args.Now,
			OnProgress:  args.OnProgress,
		})
		if err != nil {
			report.Errors = append(report.Errors, err.Error())

			continue
		}

		if result.Skipped == "locked" {
			report.Locked = true

			continue
		}

		report.Synced += result.ChannelsSynced

		for _, problem := range result.Errors {
			report.Errors = append(report.Errors, problem.Channel+": "+problem.Error)
		}
	}

	return report
}

// WaitOptions is what a wait is asked for.
type WaitOptions struct {
	// Seconds is how long to keep looking; zero checks once and returns.
	Seconds int
	// Interval is the pause between looks.
	Interval time.Duration
	// Offline skips the network entirely and answers from the archive.
	Offline bool
	// All is every task in the channel, not only what is addressed to me.
	All bool
	// Refresh is the catch-up to run before each look.
	Refresh func() CatchUpReport
}

// WaitResult is what a wait found, and how many times it looked.
type WaitResult struct {
	Report  InboxReport
	Cycles  int
	CatchUp *CatchUpReport
}

// WaitForWork is the loop behind `forum_inbox { wait_seconds }` and `agent-forum
// wait`. It stops as soon as something arrives, so a loop costs one turn rather than
// one turn per poll. A broadcast another agent already claimed is visible in the
// inbox but is not what this loop is waiting for: waking on it would report work that
// is already somebody else's.
func WaitForWork(context Context, options WaitOptions) WaitResult {
	interval := options.Interval
	if interval == 0 {
		interval = 5 * time.Second
	}

	until := time.Now().Add(time.Duration(max(0, options.Seconds)) * time.Second)
	me := context.MeLabel()

	var last *CatchUpReport

	cycles := 0

	for {
		cycles++

		if !options.Offline && options.Refresh != nil {
			result := options.Refresh()
			last = &result
			// The refresh re-read the file under us, so the views below have to come
			// from a context that sees what it wrote.
			if fresh, err := context.Reload(); err == nil {
				context = fresh
			}
		}

		report := InboxEntries(context, LoadViews(context, ViewFilter{}), InboxOptions{All: options.All})

		if actionable(report, me) || !time.Now().Before(until) {
			return WaitResult{Report: report, Cycles: cycles, CatchUp: last}
		}

		pause := min(interval, max(0, time.Until(until)))
		if pause <= 0 {
			return WaitResult{Report: report, Cycles: cycles, CatchUp: last}
		}

		time.Sleep(pause)
	}
}

// actionable is whether anything in the report is still somebody's to pick up.
func actionable(report InboxReport, me string) bool {
	for _, entry := range report.Entries {
		if entry.Task.TakenBy == "" || entry.Task.TakenBy == me {
			return true
		}
	}

	return false
}
