package forum

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/cruizba/slack-browser-mcp/internal/archive"
	"github.com/cruizba/slack-browser-mcp/internal/slack"
)

// Taking back and correcting what this machine said.
//
// Slack cannot swap the attachment of a message, and an envelope is an upload: the
// file is the message. So editing a body is a new version posted in the same place
// plus the old one deleted, which is why it is refused once the thread has answers —
// those hang off the ts of the message that would disappear.
const tsShape = `^\d{10,}\.\d{4,}$`

var tsPattern = regexp.MustCompile(tsShape)

// RemovalResult is either what a delete would do, or what it did.
type RemovalResult struct {
	Posted bool
	Text   string
}

// RemoveArgs is what `delete` is asked for.
type RemoveArgs struct {
	Ref     string
	Force   bool
	Confirm bool
}

// EditArgs is what `edit` is asked for.
type EditArgs struct {
	ID      string
	Body    string
	Confirm bool
}

// locatedMessage is the message a removal points at, and the task it may belong to.
type locatedMessage struct {
	view    BindingView
	ts      string
	task    *Task
	message *archive.ArchivedMessage
}

func locateMessage(context Context, ref string) (locatedMessage, error) {
	views := LoadViews(context, ViewFilter{})
	if len(views) == 0 {
		return locatedMessage{}, failWrite("No forum channel bound on this machine.", "Bind one: `agent-forum add <channel>`.")
	}

	value := strings.TrimSpace(ref)
	if value == "" {
		return locatedMessage{}, failWrite("delete needs a task id or a message ts.", "")
	}

	if tsPattern.MatchString(value) {
		return locateByTS(context, views, value)
	}

	located, err := LocateTask(views, value)
	if err != nil {
		return locatedMessage{}, err
	}

	records, _ := context.Archive.Read(located.View.Binding.Workspace, located.View.Binding.Channel)

	found := messageAt(records, located.Task.TS)

	return locatedMessage{view: located.View, ts: located.Task.TS, task: &located.Task, message: found}, nil
}

func locateByTS(context Context, views []BindingView, value string) (locatedMessage, error) {
	for _, view := range views {
		records, _ := context.Archive.Read(view.Binding.Workspace, view.Binding.Channel)

		message := messageAt(records, value)
		if message == nil {
			continue
		}

		if message.DeletedAt != "" {
			return locatedMessage{}, failWrite("Message "+value+" is already marked deleted.", "Nothing left to do.")
		}

		var task *Task

		for index := range view.Ledger.Tasks {
			if view.Ledger.Tasks[index].TS == value {
				task = &view.Ledger.Tasks[index]

				break
			}
		}

		return locatedMessage{view: view, ts: value, task: task, message: message}, nil
	}

	return locatedMessage{}, failWrite("No message with ts "+value+" in the bound channels.",
		"`agent-forum task <id>` prints the ts of a task, and `read <channel>` lists the rest.")
}

func messageAt(records []archive.ArchivedMessage, ts string) *archive.ArchivedMessage {
	for index := range records {
		if records[index].TS == ts {
			return &records[index]
		}
	}

	return nil
}

// markDeleted records the deletion the caller just performed, so the archive stops
// serving a message Slack no longer has.
func markDeleted(context Context, view BindingView, ts string) {
	stamp := context.Now.UTC().Format("2006-01-02T15:04:05.000Z")

	// The message is already gone from Slack; a failed cache write must not turn a
	// finished deletion into an error the caller retries.
	_ = context.Archive.Append(view.Binding.Workspace, view.Binding.Channel, []archive.ArchivedMessage{{
		TS: ts, Chan: view.Binding.Channel, ChannelName: view.Binding.Name,
		DeletedAt: stamp, SyncedAt: stamp,
	}})
}

// removable refuses what Slack or the thread would refuse, before anything is
// deleted, and answers how many replies would be orphaned.
func removable(located locatedMessage, transport Transport, force bool) (int, error) {
	author := ""

	if located.message != nil {
		author = located.message.User
	} else if located.task != nil {
		author = located.task.Owner
	}

	// chat.delete refuses anybody else's message, so checking first turns a cryptic API
	// error into a sentence.
	if author != "" && author != transport.Account.UserID {
		return 0, failWrite(
			"That message was posted by another account, and Slack only lets its author delete it.",
			"Delete it from that account, or ask whoever posted it.")
	}

	replies := 0

	if located.task != nil {
		replies = len(located.task.Events)
	} else if located.message != nil && located.message.ReplyCount != nil {
		replies = *located.message.ReplyCount
	}

	if replies > 0 && !force {
		return 0, failWrite(
			"That message is the root of a thread with "+plural(replies, "reply", "replies")+".",
			"Deleting it leaves them answering nothing. Pass --force if that is what you want, "+
				"or cancel the task so the other side sees why it stopped.")
	}

	return replies, nil
}

func excerptOf(located locatedMessage) string {
	if located.task != nil {
		return located.task.Body
	}

	if located.message != nil {
		return located.message.Body()
	}

	return ""
}

// describeMessage is what this message is, for a sentence a person reads before
// approving.
func describeMessage(located locatedMessage, binding Binding) string {
	where := binding.Name + " (" + binding.Channel + ") · ts " + located.ts

	if located.task != nil {
		return "task " + located.task.ID + " \"" + located.task.Title + "\" in " + where
	}

	return "message in " + where
}

// rewritable refuses an edit that would leave a thread hanging: replacing the root of
// a message that no longer exists is worse than a typo.
func rewritable(located Located, resolve TransportResolver) (Transport, error) {
	transport, err := resolve(located.View.Binding.Workspace)
	if err != nil {
		return Transport{}, err
	}

	if located.Task.Owner != "" && located.Task.Owner != transport.Account.UserID {
		return Transport{}, failWrite(
			"That task was posted by another account, and Slack only lets its author delete the old message.",
			"Ask whoever posted it to correct it in their own thread.")
	}

	if len(located.Task.Events) > 0 {
		return Transport{}, failWrite(
			"Task "+located.Task.ID+" already has "+plural(len(located.Task.Events), "reply", "replies")+
				" in its thread.",
			"Replacing the envelope would leave that thread hanging off a message that no longer "+
				"exists. Correct it with `reopen`, or post a `progress` note.")
	}

	return transport, nil
}

func plural(count int, one, many string) string {
	word := many
	if count == 1 {
		word = one
	}

	return strconv.Itoa(count) + " " + word
}

// DeletePosting removes a message this account posted, or previews that.
func DeletePosting(context Context, resolve TransportResolver, args RemoveArgs) (RemovalResult, error) {
	located, err := locateMessage(context, args.Ref)
	if err != nil {
		return RemovalResult{}, err
	}

	transport, err := resolve(located.view.Binding.Workspace)
	if err != nil {
		return RemovalResult{}, err
	}

	replies, err := removable(located, transport, args.Force)
	if err != nil {
		return RemovalResult{}, err
	}

	target := describeMessage(located, located.view.Binding)

	orphan := ""
	if replies > 0 {
		orphan = ", leaving " + plural(replies, "reply", "replies") + " without a parent"
	}

	if !args.Confirm {
		return RemovalResult{
			Text: "dry run — would delete " + target + orphan + "\n\n" + excerptOf(located) +
				"\n\nnothing deleted; add --post to commit",
		}, nil
	}

	if err := transport.Client.DeleteMessage(located.view.Binding.Channel, located.ts); err != nil {
		return RemovalResult{}, err
	}

	threadTS := ""
	if located.message != nil {
		threadTS = located.message.ThreadTS
	}

	audit, err := slack.RecordRemoval(context.AuditLog, slack.RemovalRecord{
		Action:    "delete",
		Workspace: transport.Account.TeamName,
		Channel:   "#" + located.view.Binding.Name,
		ChannelID: located.view.Binding.Channel,
		MessageTS: located.ts,
		ThreadTS:  threadTS,
		Text:      excerptOf(located),
	}, context.Now)
	if err != nil {
		return RemovalResult{}, err
	}

	markDeleted(context, located.view, located.ts)

	return RemovalResult{Posted: true, Text: "deleted " + target + orphan + "; audit: " + audit}, nil
}

// EditTaskBody replaces a task's body, which is a new envelope plus the deletion of
// the old message: Slack cannot swap an attachment.
func EditTaskBody(context Context, resolve TransportResolver, args EditArgs) (RemovalResult, error) {
	views := LoadViews(context, ViewFilter{})
	if len(views) == 0 {
		return RemovalResult{}, failWrite("No forum channel bound on this machine.", "Bind one: `agent-forum add <channel>`.")
	}

	located, err := LocateTask(views, args.ID)
	if err != nil {
		return RemovalResult{}, err
	}

	records, _ := context.Archive.Read(located.View.Binding.Workspace, located.View.Binding.Channel)

	message := messageAt(records, located.Task.TS)
	if message == nil || message.DeletedAt != "" {
		return RemovalResult{}, failWrite(
			"This machine has no live copy of the message behind "+located.Task.ID+".",
			"Sync the channel first: `agent-forum poll`.")
	}

	envelope := EnvelopeOf(CarrierOf(*message))
	if envelope == nil || envelope.Kind != KindTask {
		return RemovalResult{}, failWrite(located.Task.ID+" is not a task this machine can rewrite.", "")
	}

	transport, err := rewritable(located, resolve)
	if err != nil {
		return RemovalResult{}, err
	}

	where := located.View.Binding.Name + " (" + located.View.Binding.Channel + ")"

	if !args.Confirm {
		return RemovalResult{
			Text: "dry run — would replace the body of " + located.Task.ID + " in " + where +
				" and delete the previous message (ts " + located.Task.TS + ")\n\n" +
				"now:\n" + located.Task.Body + "\n\nwould be:\n" + args.Body + "\n\n" +
				"nothing sent; add --post to commit",
		}, nil
	}

	// New version first: a failure between the two calls leaves two live copies, which
	// the ledger resolves by newest, instead of none.
	replacement := *envelope
	replacement.ID = NewID(context.Now)
	replacement.Body = args.Body
	replacement.HasBody = true

	posted, err := Deliver(context, transport, located.View.Binding, replacement, "")
	if err != nil {
		return RemovalResult{}, err
	}

	if err := transport.Client.DeleteMessage(located.View.Binding.Channel, located.Task.TS); err != nil {
		return RemovalResult{}, err
	}

	audit, err := slack.RecordRemoval(context.AuditLog, slack.RemovalRecord{
		Action:    "edit",
		Workspace: transport.Account.TeamName,
		Channel:   "#" + located.View.Binding.Name,
		ChannelID: located.View.Binding.Channel,
		MessageTS: located.Task.TS,
		Text:      located.Task.Body,
	}, context.Now)
	if err != nil {
		return RemovalResult{}, err
	}

	markDeleted(context, located.View, located.Task.TS)

	text := "replaced the body of " + located.Task.ID + " in " + where

	if posted.TS != "" {
		text += " at ts " + posted.TS
	} else {
		text += " (Slack reported no ts; it is posted)"
	}

	return RemovalResult{Posted: true, Text: text + "; audit: " + audit}, nil
}
