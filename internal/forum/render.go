package forum

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// maxInlineBodyChars is how much of a body `forum task` prints before pointing at
// the file. An agent skimming a thread needs the beginning and a way to the rest,
// not a megabyte of pasted logs.
const maxInlineBodyChars = 4000

// Ago is how long ago a ts was, in the shorthand these lines use.
func Ago(ts string, now time.Time) string {
	then := numeric(ts) * 1000
	if then == 0 {
		return "unknown"
	}

	minutes := max(0, int(math.Round((float64(now.UnixMilli())-then)/60_000)))

	switch {
	case minutes < 1:
		return "now"
	case minutes < 60:
		return strconv.Itoa(minutes) + "m"
	}

	hours := int(math.Round(float64(minutes) / 60))
	if hours < 48 {
		return strconv.Itoa(hours) + "h"
	}

	return strconv.Itoa(int(math.Round(float64(hours)/24))) + "d"
}

// DecisionLabel is what the local ledger says about a task, distinct from what the
// channel says: the disagreement between the two is the point of keeping both.
func DecisionLabel(decision Decision, decided bool) string {
	if !decided {
		return "local:-"
	}

	self := ""
	if decision.SelfApproved {
		self = "(self)"
	}

	return "local:" + decision.Decision + self
}

// PeerLabel is an agent as a person reads it: the agent name, plus the person behind
// it when the cached directory can name the Slack account that registered it.
func PeerLabel(from string, view *BindingView) string {
	if view == nil {
		return from
	}

	owner := ""

	for _, peer := range view.Roster {
		if peer.From == from {
			owner = peer.Owner

			break
		}
	}

	if owner == "" {
		return from
	}

	person := owner
	if label := view.People[owner]; label != "" {
		person = label
	}

	return from + " (" + person + ")"
}

// ChannelLabel is how a bound channel prints.
func ChannelLabel(binding Binding) string {
	prefix := "#"
	if binding.Private {
		prefix = "private:"
	}

	return prefix + binding.Name
}

// RenderPeers is what "who is on the forum" looks like after a plain read.
func RenderPeers(view BindingView, now time.Time, limits Limits) []string {
	lines := []string{ChannelLabel(view.Binding) + " (" + view.Binding.Channel + ")"}

	if len(view.Roster) == 0 {
		return append(lines, "  nobody registered yet")
	}

	for _, peer := range view.Roster {
		person := ""
		if peer.Owner != "" {
			person = peer.Owner
			if label := view.People[peer.Owner]; label != "" {
				person = label
			}
		}

		who := ""
		if person != "" {
			who = " · " + person
		}

		lines = append(lines, "  "+peer.From+who+" — "+peerState(peer, now, limits)+capabilitiesOf(peer))
	}

	return lines
}

func peerState(peer Peer, now time.Time, limits Limits) string {
	seen := Ago(peer.TS, now)
	active := Ago(peer.InteractiveTS, now)

	note := ""
	if peer.Note != "" {
		note = " — " + peer.Note
	}

	if MachineStatusOf(peer, float64(limits.PeerTTLHours), now) == MachineOut {
		return "out, last seen " + seen + " ago"
	}

	switch HumanStatusOf(peer, limits.HereSeconds, float64(limits.PeerTTLHours), now) {
	case HumanAway:
		return "up · away" + note
	case HumanHere:
		if active == "now" {
			return "up · here, active now"
		}

		return "up · here, active " + active + " ago"
	case HumanUnknown:
		return "up, last seen " + seen + " ago"
	default:
		return "up, last seen " + seen + " ago"
	}
}

func capabilitiesOf(peer Peer) string {
	if len(peer.Capabilities) == 0 {
		return ""
	}

	return " · can " + strings.Join(peer.Capabilities, ", ")
}

// InboxLine is one task as the inbox prints it.
func InboxLine(task Task, decision Decision, decided bool, now time.Time, withChannel string) string {
	channel := ""
	if withChannel != "" {
		channel = withChannel + " · "
	}

	taken := ""
	if task.TakenBy != "" {
		taken = " taken:" + task.TakenBy
	}

	needs := ""
	if task.Capability != "" {
		needs = "[" + task.Capability + "] "
	}

	return fmt.Sprintf("%s%s  %-9s %-16s <- %s%s  %s%s  %s",
		channel, task.ID, StateLabel(task, now), DecisionLabel(decision, decided),
		task.From, taken, needs, task.Title, Ago(task.TS, now))
}

// RenderInbox is the inbox as both surfaces print it.
func RenderInbox(context Context, report InboxReport, offline bool, note string) string {
	me := context.MeLabel()
	if me == "" {
		me = "unregistered"
	}

	bindings := make([]string, 0, len(report.Views))
	for _, view := range report.Views {
		bindings = append(bindings, ChannelLabel(view.Binding)+" ("+view.Binding.Channel+")")
	}

	counts := []string{strconv.Itoa(len(report.Entries)) + " task(s)"}

	if report.Taken > 0 {
		counts = append(counts, strconv.Itoa(report.Taken)+" taken")
	}

	if report.Hidden > 0 {
		counts = append(counts, strconv.Itoa(report.Hidden)+" from unknown or retired peers")
	}

	if report.Unreadable > 0 {
		counts = append(counts, strconv.Itoa(report.Unreadable)+" unreadable envelope(s)")
	}

	if report.Orphans > 0 {
		counts = append(counts, strconv.Itoa(report.Orphans)+" orphan repl(y/ies)")
	}

	head := "agent forum as " + me
	if len(bindings) > 0 {
		head += " · " + strings.Join(bindings, ", ")
	} else {
		head += " · no channel bound"
	}

	if offline {
		head += " · offline"
	}

	head += note

	lines := make([]string, 0, len(report.Entries))

	for _, entry := range report.Entries {
		channel := ""
		if len(report.Views) > 1 {
			channel = ChannelLabel(entry.View.Binding)
		}

		lines = append(lines, InboxLine(entry.Task, entry.Decision, entry.Decided, context.Now, channel))
	}

	body := "nothing pending"
	if len(lines) > 0 {
		body = strings.Join(lines, "\n")
	}

	return head + "\n" + strings.Join(counts, " · ") + "\n" + body
}

// RenderTask is `forum task <id>`.
func RenderTask(context Context, task Task, binding Binding, decision Decision, decided bool, view *BindingView) string {
	header := task.ID + "  " + StateLabel(task, context.Now) + "  " + DecisionLabel(decision, decided) +
		"  " + binding.Workspace + "/" + ChannelLabel(binding)

	person := ""
	if task.Owner != "" {
		person = task.Owner
		if view != nil {
			if label := view.People[task.Owner]; label != "" {
				person = label
			}
		}
	}

	route := task.From
	if person != "" {
		route += " (" + person + ")"
	}

	route += " -> " + task.To + " · sent " + task.Sent

	if task.Capability != "" {
		route += " · needs " + task.Capability
	}

	if task.EditedAt != "" {
		route += " · edited"
	}

	if task.Deadline != "" {
		route += " · deadline " + task.Deadline
	}

	if task.Reopens > 0 {
		route += " · reopened " + strconv.Itoa(task.Reopens) + "x"
	}

	thread := ""
	if len(task.Events) > 0 {
		thread = "\n\n" + eventLines(task)
	}

	ignored := ""

	if len(task.Ignored) > 0 {
		labels := make([]string, 0, len(task.Ignored))

		for _, event := range task.Ignored {
			labels = append(labels, event.Kind+" from "+event.From)
		}

		ignored = "\n\n  " + strconv.Itoa(len(task.Ignored)) + " ignored event(s): " + strings.Join(labels, ", ")
	}

	return header + "\n" + route + "\n\n" + task.Title + "\n" +
		bodyForContext(task.Body, task.FileID) + thread + ignored
}

func eventLines(task Task) string {
	lines := make([]string, 0, len(task.Events))

	for _, event := range task.Events {
		body := ""

		if event.Body != "" {
			first, _, _ := strings.Cut(event.Body, "\n")
			body = ": " + first
		}

		lines = append(lines, "  ["+event.TS+"] "+event.Kind+" from "+event.From+body)
	}

	return strings.Join(lines, "\n")
}

// bodyForContext cuts a body that would bury the thread it belongs to, and says
// where the rest lives.
func bodyForContext(body, fileID string) string {
	size := len([]rune(body))
	if size <= maxInlineBodyChars {
		return body
	}

	where := "the whole envelope is in the local archive"
	if fileID != "" {
		where = "the whole envelope is `read_file " + fileID + "`"
	}

	return string([]rune(body)[:maxInlineBodyChars]) + "… [truncated: " +
		strconv.Itoa(size) + " characters; " + where + "]"
}
