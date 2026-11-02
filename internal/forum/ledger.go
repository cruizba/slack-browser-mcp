package forum

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/archive"
	"github.com/cruizba/slack-browser-mcp/internal/failure"
)

// TaskState is where one task stands, derived from the thread every time it is
// read. Nothing is stored: a `task` envelope opens a task, replies move it, and a
// missing reply is a task that never moved. That is what makes the thread the
// record — a crashed agent cannot leave a ledger that disagrees with the channel.
type TaskState string

// The states a task moves through.
const (
	StateOpen         TaskState = "open"
	StateAccepted     TaskState = "accepted"
	StateDeclined     TaskState = "declined"
	StateDone         TaskState = "done"
	StateFailed       TaskState = "failed"
	StateAcknowledged TaskState = "acknowledged"
	StateCancelled    TaskState = "cancelled"
	StateExpired      TaskState = "expired"
	// StateDeleted is a task whose envelope is gone from the channel; the task is kept
	// so its thread still reads.
	StateDeleted TaskState = "deleted"
)

// closedStates mean nobody is waiting for anything else; done and failed are not
// among them, because the requester has still to acknowledge them.
var closedStates = map[TaskState]bool{
	StateDeclined:     true,
	StateAcknowledged: true,
	StateCancelled:    true,
	StateExpired:      true,
	StateDeleted:      true,
}

var reportedStates = map[TaskState]bool{StateDone: true, StateFailed: true}

// TaskEvent is one reply as the ledger keeps it.
type TaskEvent struct {
	Kind string
	From string
	TS   string
	Sent string
	Body string
	OK   *bool
}

// Task is one row of the ledger, and everything `task <id>` prints.
type Task struct {
	ID      string
	Team    string
	Channel string
	TS      string
	From    string
	To      string
	// Capability is what the task needs; Tag is its older name on a broadcast.
	Capability string
	Title      string
	Body       string
	// FileID is the `forum-*` attachment, when the envelope arrived as a file.
	FileID   string
	Deadline string
	Parent   string
	Depth    int
	Sent     string
	// Owner is the Slack user whose agent posted the task.
	Owner   string
	State   TaskState
	Closed  bool
	Reopens int
	TakenBy string
	Events  []TaskEvent
	Ignored []TaskEvent

	LastActivityTS string
	Stalled        bool
	// EditedAt is the message ts of the replacement, when this task's envelope was
	// edited.
	EditedAt string
}

// Ledger is every task a window of the channel holds, and how much of it could not
// be read.
type Ledger struct {
	Tasks []Task
	// Unreadable counts messages that claim an envelope but carry no readable one.
	Unreadable int
	// Orphans counts replies whose task is not in this window.
	Orphans int
	// Unvouched counts readable envelopes whose author is not in the roster.
	Unvouched int
}

// LedgerArgs is what a derivation needs: the window, who is on the channel, and the
// clocks.
type LedgerArgs struct {
	Messages  []archive.ArchivedMessage
	TeamID    string
	ChannelID string
	Roster    []Peer
	Me        string
	Limits    Limits
	Now       time.Time
}

// numeric is Slack's ts order, which is what a comparison between two messages
// means.
func numeric(ts string) float64 {
	value, err := strconv.ParseFloat(ts, 64)
	if err != nil {
		return 0
	}

	return value
}

func secondsOf(ts string) float64 {
	value, err := strconv.ParseFloat(ts, 64)
	if err != nil {
		return 0
	}

	return value * 1000
}

// epochOf reads `sent` and `deadline`, which are ISO 8601, while `ts` is Slack's
// numeric stamp and is read elsewhere.
func epochOf(value string) float64 {
	if numeric, err := strconv.ParseFloat(value, 64); err == nil && numeric > 1_000_000 {
		return numeric * 1000
	}

	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0
	}

	return float64(parsed.UnixMilli())
}

func isAddressee(task *Task, from string) bool {
	return task.To == "*" || task.To == from
}

// isClaimant keeps a claimed broadcast to its claimant: anybody else's note is not
// its outcome.
func isClaimant(task *Task, from string) bool {
	return task.TakenBy == "" || task.TakenBy == from
}

// ApplyEvent moves the task when the sender is the side allowed to move it. It
// answers whether the event moved anything; what it did not move is kept aside
// rather than dropped, so a spoofed `accept` shows up instead of vanishing.
func ApplyEvent(task *Task, event TaskEvent) bool {
	addressee := isAddressee(task, event.From)
	requester := event.From == task.From

	switch event.Kind {
	case KindAccept:
		return applyAccept(task, event, addressee)
	case KindDecline:
		return applyDecline(task, event, addressee)
	case KindProgress:
		// A conversation needs both sides able to say something in the thread: the
		// worker answering, the requester clarifying, neither moving the state.
		return task.State == StateAccepted && (addressee || requester)
	case KindResult:
		return applyResult(task, event, addressee)
	case KindError:
		return applyError(task, event, addressee)
	case KindAck:
		return applyAck(task, requester)
	case KindReopen:
		return applyReopen(task, requester)
	case KindCancel:
		return applyCancel(task, requester)
	}

	return false
}

func applyAccept(task *Task, event TaskEvent, addressee bool) bool {
	if task.State != StateOpen || !addressee {
		return false
	}

	task.State = StateAccepted
	task.TakenBy = event.From

	return true
}

// applyDecline refuses a broadcast: one agent's no would close the request for
// everybody else in the channel.
func applyDecline(task *Task, _ TaskEvent, addressee bool) bool {
	if task.State != StateOpen || !addressee || task.To == "*" {
		return false
	}

	task.State = StateDeclined

	return true
}

func applyResult(task *Task, event TaskEvent, addressee bool) bool {
	if task.State != StateAccepted || !addressee || !isClaimant(task, event.From) {
		return false
	}

	if event.OK != nil && !*event.OK {
		task.State = StateFailed
	} else {
		task.State = StateDone
	}

	return true
}

func applyError(task *Task, event TaskEvent, addressee bool) bool {
	if task.State != StateAccepted || !addressee || !isClaimant(task, event.From) {
		return false
	}

	task.State = StateFailed

	return true
}

func applyAck(task *Task, requester bool) bool {
	if !reportedStates[task.State] || !requester {
		return false
	}

	task.State = StateAcknowledged

	return true
}

func applyReopen(task *Task, requester bool) bool {
	if !reportedStates[task.State] || !requester {
		return false
	}

	task.State = StateAccepted
	task.Reopens++

	return true
}

func applyCancel(task *Task, requester bool) bool {
	if (task.State != StateOpen && task.State != StateAccepted) || !requester {
		return false
	}

	task.State = StateCancelled

	return true
}

// BuildLedger derives the state of every task in a window.
func BuildLedger(args LedgerArgs) Ledger {
	builder := &ledgerBuilder{
		args:   args,
		byID:   map[string]*Task{},
		order:  make([]string, 0, len(args.Messages)),
		ledger: Ledger{},
	}

	for _, message := range args.Messages {
		builder.message(message)
	}

	builder.finish()

	return builder.ledger
}

// ledgerBuilder folds one window of messages into tasks.
type ledgerBuilder struct {
	args   LedgerArgs
	byID   map[string]*Task
	order  []string
	ledger Ledger
}

func (b *ledgerBuilder) message(message archive.ArchivedMessage) {
	carrier := CarrierOf(message)
	envelope := EnvelopeOf(carrier)

	if envelope == nil {
		// A message that no longer carries a readable envelope and is gone anyway says
		// nothing; only a live one is the defect the counter is for.
		if message.DeletedAt == "" && CarriesEnvelope(carrier) {
			b.ledger.Unreadable++
		}

		return
	}

	// The roster kinds carry no state of their own here; they only say who exists.
	if envelope.Kind == KindRegister || envelope.Kind == KindBeacon || envelope.Kind == KindPresence {
		return
	}

	// A deleted task keeps its entry so `task <id>` still answers and its replies are
	// not orphans; a deleted reply is just an event that stopped existing.
	if message.DeletedAt != "" && envelope.Kind != KindTask {
		return
	}

	if !b.vouched(envelope) {
		b.ledger.Unvouched++

		return
	}

	if envelope.Kind == KindTask {
		openTask(b.byID, &b.order, b.args, message, envelope)

		return
	}

	b.reply(message, envelope)
}

func (b *ledgerBuilder) vouched(envelope *Envelope) bool {
	if envelope.From == b.args.Me {
		return true
	}

	_, known := PeerFor(b.args.Roster, envelope.From)

	return known
}

func (b *ledgerBuilder) reply(message archive.ArchivedMessage, envelope *Envelope) {
	task, known := b.byID[envelope.Task]
	if !known {
		b.ledger.Orphans++

		return
	}

	event := TaskEvent{
		Kind: envelope.Kind,
		From: envelope.From,
		TS:   message.TS,
		Sent: envelope.Sent,
		Body: envelope.Body,
	}

	if envelope.OK != nil {
		event.OK = envelope.OK
	}

	if task.State == StateDeleted {
		// Nothing can move a task whose envelope is gone, but what was said in the
		// thread still happened and `task <id>` shows it.
		task.Events = append(task.Events, event)
		task.LastActivityTS = message.TS

		return
	}

	if ApplyEvent(task, event) {
		task.Events = append(task.Events, event)
		task.LastActivityTS = message.TS

		return
	}

	task.Ignored = append(task.Ignored, event)
}

func (b *ledgerBuilder) finish() {
	_, _, ttlMS := Timings(b.args.Limits)

	tasks := make([]Task, 0, len(b.byID))

	for _, id := range b.order {
		task, known := b.byID[id]
		if !known {
			continue
		}

		expire(task, b.args.Now, ttlMS)
		task.Closed = closedStates[task.State]
		task.Stalled = stalled(task, b.args.Limits, b.args.Now)

		tasks = append(tasks, *task)
	}

	sort.SliceStable(tasks, func(left, right int) bool {
		return numeric(tasks[left].TS) < numeric(tasks[right].TS)
	})

	b.ledger.Tasks = tasks
}

func openTask(
	byID map[string]*Task,
	order *[]string,
	args LedgerArgs,
	message archive.ArchivedMessage,
	envelope *Envelope,
) {
	if _, known := byID[envelope.ID]; !known {
		capability := envelope.Capability
		if capability == "" {
			capability = envelope.Tag
		}

		state := StateOpen
		if message.DeletedAt != "" {
			state = StateDeleted
		}

		fileID := ""
		if name, carried := envelopeNameOf(message.Files); carried {
			fileID = name
		}

		depth := 0
		if envelope.Depth != nil {
			depth = *envelope.Depth
		}

		byID[envelope.ID] = &Task{
			ID:             envelope.ID,
			Team:           args.TeamID,
			Channel:        args.ChannelID,
			TS:             message.TS,
			From:           envelope.From,
			To:             envelope.To,
			Capability:     capability,
			Title:          envelope.Title,
			Body:           envelope.Body,
			FileID:         fileID,
			Deadline:       envelope.Deadline,
			Parent:         envelope.Parent,
			Depth:          depth,
			Sent:           envelope.Sent,
			Owner:          message.User,
			State:          state,
			Closed:         message.DeletedAt != "",
			LastActivityTS: message.TS,
		}

		*order = append(*order, envelope.ID)

		return
	}

	// A re-posted task id is the same task: the later envelope wins the header, the
	// earlier events stay, so an edit cannot reset a claim.
	existing := byID[envelope.ID]

	if message.DeletedAt != "" || numeric(message.TS) <= numeric(existing.TS) {
		return
	}

	existing.TS = message.TS
	existing.Title = envelope.Title
	existing.Body = envelope.Body
	existing.Sent = envelope.Sent

	if name, carried := envelopeNameOf(message.Files); carried {
		existing.FileID = name
	}

	if existing.State == StateDeleted {
		existing.State = StateOpen
		existing.Closed = false
	}

	existing.EditedAt = message.TS
}

func expire(task *Task, now time.Time, ttlMS int64) {
	if closedStates[task.State] || reportedStates[task.State] {
		return
	}

	age := float64(now.UnixMilli()) - epochOf(task.Sent)
	deadlinePassed := task.Deadline != "" && epochOf(task.Deadline) < float64(now.UnixMilli())

	if deadlinePassed || age > float64(ttlMS) {
		task.State = StateExpired
	}
}

func stalled(task *Task, limits Limits, now time.Time) bool {
	ackMS, stallMS, _ := Timings(limits)

	silence := float64(now.UnixMilli()) - secondsOf(task.LastActivityTS)

	if task.State == StateOpen {
		return silence > float64(ackMS)
	}

	if task.State == StateAccepted {
		return silence > float64(stallMS)
	}

	return false
}

// MatchingTasks is every task a prefix could name.
func MatchingTasks(tasks []Task, prefix string) []Task {
	matching := make([]Task, 0, 2)

	for _, task := range tasks {
		if MatchesID(task.ID, prefix) {
			matching = append(matching, task)
		}
	}

	return matching
}

// FindTask answers the one task a prefix names, or explains why it cannot.
func FindTask(tasks []Task, prefix string) (Task, error) {
	matching := MatchingTasks(tasks, prefix)

	switch len(matching) {
	case 1:
		return matching[0], nil
	case 0:
		hint := "This channel has no tasks yet."
		if len(tasks) > 0 {
			hint = strconv.Itoa(len(tasks)) + " task(s) are known here; run `agent-forum inbox --all` to list them."
		}

		return Task{}, &failure.ForumError{Message: "No task matching \"" + prefix + "\".", Hint: hint}
	default:
		labels := make([]string, 0, 5)

		for _, task := range matching {
			if len(labels) == 5 {
				break
			}

			labels = append(labels, task.ID+" ("+task.Title+")")
		}

		return Task{}, &failure.ForumError{
			Message: "\"" + prefix + "\" matches " + strconv.Itoa(len(matching)) + " tasks: " +
				strings.Join(labels, ", ") + ".",
			Hint: "Pass a longer prefix.",
		}
	}
}

// StateLabel is the state as a reader sees it, with how long it has been silent.
func StateLabel(task Task, now time.Time) string {
	if !task.Stalled {
		return string(task.State)
	}

	silent := int(math.Round((float64(now.UnixMilli()) - secondsOf(task.LastActivityTS)) / 60_000))

	return string(task.State) + "(stalled " + strconv.Itoa(silent) + "m)"
}
