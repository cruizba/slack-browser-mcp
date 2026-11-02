package forum

import (
	"sort"
	"strconv"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/slack"
)

// BindingView is one bound channel as the inbox reads it: who is on it, what the
// ledger derived, and what was decided locally.
type BindingView struct {
	Binding   Binding
	Key       string
	Roster    []Peer
	Ledger    Ledger
	Decisions map[string]Decision
	// People maps a Slack user id to a readable name, from the cached directory when
	// it exists.
	People map[string]string
}

// LoadView reads one bound channel.
func LoadView(context Context, binding Binding) BindingView {
	messages, _ := context.Archive.Read(binding.Workspace, binding.Channel)

	roster := RosterFrom(messages, context.File.Ignored)

	ledger := BuildLedger(LedgerArgs{
		Messages:  messages,
		TeamID:    binding.Workspace,
		ChannelID: binding.Channel,
		Roster:    roster,
		Me:        context.MeLabel(),
		Limits:    context.Limits,
		Now:       context.Now,
	})

	decisions, _ := context.Decision.Read(binding.Workspace, binding.Channel)

	directory, _ := slack.NewDirectoryStore(context.Archive.Root).Read(binding.Workspace)

	people := map[string]string{}

	for _, peer := range roster {
		if peer.Owner == "" {
			continue
		}

		if label := directory.User(peer.Owner); label != "" {
			people[peer.Owner] = label
		}
	}

	return BindingView{
		Binding:   binding,
		Key:       binding.Key(),
		Roster:    roster,
		Ledger:    ledger,
		Decisions: decisions,
		People:    people,
	}
}

// ViewFilter names the channel a caller wants, if it wants one.
type ViewFilter struct {
	Workspace string
	Only      string
}

// LoadViews answers the views a filter names.
func LoadViews(context Context, filter ViewFilter) []BindingView {
	views := make([]BindingView, 0, len(context.File.Channels))

	for _, binding := range context.File.Channels {
		if filter.Workspace != "" && binding.Workspace != filter.Workspace {
			continue
		}

		if filter.Only != "" && binding.Key() != filter.Only {
			continue
		}

		views = append(views, LoadView(context, binding))
	}

	return views
}

// InboxEntry is one task the reader has to look at.
type InboxEntry struct {
	View     BindingView
	Task     Task
	Decision Decision
	Decided  bool
}

// InboxOptions is what an inbox is asked for.
type InboxOptions struct {
	// All is everything in the bound channels, not only what is addressed to me.
	All bool
	// SinceStart ignores the stored boundary and shows the whole history the binding
	// can see.
	SinceStart bool
}

// InboxReport is what an inbox found, and how much it could not read.
type InboxReport struct {
	Entries []InboxEntry
	Views   []BindingView
	// Unreadable counts messages that claim an envelope but carry no readable one.
	Unreadable int
	Orphans    int
	// Hidden counts tasks addressed to me whose author is not in the roster, or is
	// retired.
	Hidden int
	// Taken counts `*` tasks another agent already claimed; they are listed with
	// `taken:<peer>` rather than hidden, because hiding them is what makes a second
	// agent think nobody took the work.
	Taken int
}

// InboxEntries derives the list. The filter is state-based, not cursor-based: a task
// shows up while it is open and addressed to me, and disappears when it closes, so
// calling the inbox twice returns the same pending set instead of an empty second
// answer. The boundary only says how far back a scan goes.
func InboxEntries(context Context, views []BindingView, options InboxOptions) InboxReport {
	me := context.MeLabel()

	report := InboxReport{Views: views}

	for _, view := range views {
		report.Unreadable += view.Ledger.Unreadable
		report.Orphans += view.Ledger.Orphans
		report.Hidden += view.Ledger.Unvouched

		// The boundary is when the channel was bound. Reading must not move it: a
		// watermark advanced by showing something is what makes the same task vanish
		// on the next call.
		floor := 0.0
		if !options.SinceStart {
			floor = numeric(view.Binding.SinceTS)
		}

		for _, task := range view.Ledger.Tasks {
			decision, decided := view.Decisions[task.ID]

			if !options.All && !inboxWants(task, me, floor, decision, decided, &report) {
				continue
			}

			report.Entries = append(report.Entries, InboxEntry{
				View: view, Task: task, Decision: decision, Decided: decided,
			})
		}
	}

	sort.SliceStable(report.Entries, func(left, right int) bool {
		return numeric(report.Entries[left].Task.TS) < numeric(report.Entries[right].Task.TS)
	})

	return report
}

// inboxWants is the filter in one place, because every clause is a reason a task
// should not be in somebody's list.
func inboxWants(task Task, me string, floor float64, decision Decision, decided bool, report *InboxReport) bool {
	mine := task.To == me || task.To == "*"

	switch {
	case !mine:
		return false
	case task.From == me:
		return false
	case numeric(task.TS) <= floor:
		return false
	case decided && decision.Decision == "rejected":
		return false
	case task.Closed:
		return false
	}

	if task.TakenBy != "" && task.TakenBy != me {
		// Still the channel's work, listed and counted, and labelled with who has it.
		report.Taken++
	}

	return true
}

// NewestTS is the highest ts an inbox saw, so the caller can advance the cursor
// without guessing.
func NewestTS(report InboxReport) string {
	if len(report.Entries) == 0 {
		return ""
	}

	return report.Entries[len(report.Entries)-1].Task.TS
}

// Now is a small helper so the decision log and the ledger share one clock.
func Now(context Context) time.Time {
	return context.Now
}

// CountOfTasks is the number an inbox line prints.
func CountOfTasks(tasks []Task) string {
	return strconv.Itoa(len(tasks))
}
