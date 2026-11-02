package forum

import (
	"strconv"
	"strings"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/archive"
	"github.com/cruizba/slack-browser-mcp/internal/auth"
	"github.com/cruizba/slack-browser-mcp/internal/config"
	"github.com/cruizba/slack-browser-mcp/internal/envelope"
	"github.com/cruizba/slack-browser-mcp/internal/failure"
	"github.com/cruizba/slack-browser-mcp/internal/slack"
)

// The workflow both surfaces call: bind, register, post, accept, report, ack, reopen.
// Every write funnels through Deliver, so the confirm gate, the AI signature, the
// audit entry and the local decision cannot be skipped by adding a command later.
//
// A write that is not confirmed returns the envelope it would post and changes
// nothing — not even the local decision. The id and timestamp are minted at post
// time, so the preview shows the content, not the bytes.
const retryDelay = 1200 * time.Millisecond

// Transport is the session a channel's writes go out from.
type Transport struct {
	Account auth.Account
	Client  *slack.Client
	Signing config.Signing
}

// TransportResolver builds a transport per channel, because a task's channel decides
// which workspace answers.
type TransportResolver func(teamID string) (Transport, error)

// RefreshHook is the catch-up a write runs before deciding: the other side may have
// accepted or reported since this machine last synced. Without one the service
// answers from the archive alone, which is what a test wants.
type RefreshHook struct {
	Refresh    func() error
	RetryDelay time.Duration
}

func freshen(context Context, hook *RefreshHook) (Context, error) {
	if hook == nil || hook.Refresh == nil {
		return context, nil
	}

	if err := hook.Refresh(); err != nil {
		return context, err
	}

	return context.Reload()
}

// WriteResult is either what a confirmed write did, or what it would do.
type WriteResult struct {
	// Posted is false for a preview.
	Posted   bool
	Envelope Envelope
	Binding  Binding
	// Text is the preview text; TS and Audit are set once posted.
	Text    string
	TS      string
	Audit   string
	Target  string
	Warning string
}

// Located is a task and the view it came from.
type Located struct {
	View BindingView
	Task Task
}

func failWrite(message, hint string) error {
	return &failure.ForumError{Message: message, Hint: hint}
}

// deadlineStamp reads a deadline, which decides when a task expires, so an
// unreadable or already-past one would kill the task on its first read.
func deadlineStamp(raw string, now time.Time) (string, error) {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return "", failWrite("\""+raw+"\" is not a date.", "A deadline is ISO 8601, for example 2026-11-20T18:00:00Z.")
	}

	if !parsed.After(now) {
		return "", failWrite("The deadline "+raw+" is already past.",
			"A task that expires on arrival is one nobody can claim.")
	}

	return parsed.UTC().Format("2006-01-02T15:04:05.000Z"), nil
}

// LocateTask answers the one task a prefix names, across every bound channel.
func LocateTask(views []BindingView, prefix string) (Located, error) {
	type candidate struct {
		view BindingView
		task Task
	}

	all := make([]candidate, 0, 16)
	matches := make([]candidate, 0, 2)

	for _, view := range views {
		for _, task := range view.Ledger.Tasks {
			entry := candidate{view: view, task: task}
			all = append(all, entry)

			if MatchesID(task.ID, prefix) {
				matches = append(matches, entry)
			}
		}
	}

	switch len(matches) {
	case 1:
		return Located{View: matches[0].view, Task: matches[0].task}, nil
	case 0:
		hint := "This forum has no tasks yet. Run `agent-forum peers` to see who is around."
		if len(all) > 0 {
			hint = strconv.Itoa(len(all)) + " task(s) known; run `agent-forum inbox --all` to list them."
		}

		return Located{}, failWrite("No task matching \""+prefix+"\".", hint)
	default:
		labels := make([]string, 0, 5)

		for _, match := range matches {
			if len(labels) == 5 {
				break
			}

			labels = append(labels, match.task.ID+" ("+match.task.Title+")")
		}

		return Located{}, failWrite("\""+prefix+"\" matches "+strconv.Itoa(len(matches))+" tasks: "+
			strings.Join(labels, ", ")+".", "Pass a longer prefix.")
	}
}

func taskAddressee(task Task, me string) bool {
	return task.To == "*" || task.To == me
}

func requireOpen(task Task, me string) error {
	if task.State != StateOpen {
		return failWrite("Task "+task.ID+" is "+string(task.State)+", not open.",
			"Only an open task can be claimed or declined.")
	}

	if !taskAddressee(task, me) {
		return failWrite("Task "+task.ID+" is addressed to "+task.To+".", "An agent can only claim what was sent to it.")
	}

	return nil
}

func requireAccepted(task Task, me string) error {
	if task.State != StateAccepted {
		hint := "Nothing more can be reported on it."
		if task.State == StateOpen {
			hint = "Claim it first with `accept`."
		}

		return failWrite("Task "+task.ID+" is "+string(task.State)+", not accepted.", hint)
	}

	if !taskAddressee(task, me) {
		return failWrite("Task "+task.ID+" was sent to "+task.To+", not to you.", "")
	}

	if task.TakenBy != "" && task.TakenBy != me {
		return failWrite("Task "+task.ID+" was claimed by "+task.TakenBy+".",
			"Only the agent that claimed it can report on it.")
	}

	return nil
}

func requireRequester(task Task, me string) error {
	if task.From != me {
		return failWrite("Task "+task.ID+" was posted by "+task.From+".", "Only the requester can do that.")
	}

	return nil
}

// Deliver renders, signs, posts and records. The only function that talks to Slack,
// which is what makes "every envelope is audited" a property of the package instead
// of a habit.
//
// It never fails because the acknowledgement is thin: Slack sometimes accepts an
// upload and reports which message it made a beat later, and an error there is an
// error that gets the same move posted twice.
func Deliver(context Context, transport Transport, binding Binding, message Envelope, threadTS string) (WriteResult, error) {
	summary := SummaryFor(&message)

	if err := AssertFits(summary); err != nil {
		return WriteResult{}, err
	}

	document, err := message.Encode()
	if err != nil {
		return WriteResult{}, err
	}

	name := envelope.FileName(message.ID)
	nickname := config.ResolveNickname(transport.Signing, relabel(context), transport.Account.User)

	text := summary

	var blocks any

	if nickname != nil && binding.Sign {
		signed, err := slack.SignedMessage(summary, *nickname)
		if err != nil {
			return WriteResult{}, err
		}

		text = signed.Text

		if len(signed.Blocks) > 0 {
			blocks = signed.Blocks
		}
	}

	posted, err := transport.Client.UploadFiles(binding.Channel, []slack.Upload{
		{Name: name, Bytes: []byte(document)},
	}, text, threadTS)
	if err != nil {
		return WriteResult{}, err
	}

	_ = blocks

	fileID := ""
	if len(posted.FileIDs) > 0 {
		fileID = posted.FileIDs[0]
	}

	audit, err := slack.RecordSend(context.AuditLog, slack.SendRecord{
		Workspace: transport.Account.TeamName,
		Channel:   "#" + binding.Name,
		ChannelID: binding.Channel,
		ThreadTS:  threadTS,
		MessageTS: posted.TS,
		Text:      text,
	}, context.Now)
	if err != nil {
		return WriteResult{}, err
	}

	result := WriteResult{Posted: true, Envelope: message, Binding: binding, TS: posted.TS, Audit: audit}

	if posted.TS == "" {
		// The message is out: Slack accepted the upload and the comment is in the
		// channel. Without its ts this machine cannot file it locally, and a caller
		// that treats that as a failure posts the same move twice.
		result.Warning = "Slack accepted the message without reporting its ts. It is posted, so do not post it " +
			"again; this machine files it on the next sync."

		return result, nil
	}

	// The poster's own archive has to know immediately: deriving the state from a copy
	// that only catches up on the next sync makes "accept, then report" fail with
	// "still open" in the same session.
	rememberOwn(context, binding, posted.TS, text, transport.Account.UserID, threadTS,
		archive.ArchivedFile{ID: fileID, Name: name, Mimetype: "text/markdown", Size: int64(len(document))}, document)

	return result, nil
}

// rememberOwn files the message this machine just posted. The cursor is left alone on
// purpose: moving it here would make the next backfill skip the gap between its own
// cursor and this message.
func rememberOwn(
	context Context,
	binding Binding,
	ts, text, user, threadTS string,
	file archive.ArchivedFile,
	document string,
) {
	channel := slack.Channel{ID: binding.Channel, Name: binding.Name}

	record := archive.ToRecord(channel, slack.Message{
		TS: ts, Text: text, User: user, ThreadTS: threadTS,
	}, context.Now, "")

	record.Files = []archive.ArchivedFile{file}
	record.Forum = document

	// A failed cache write must never fail a send that already reached Slack.
	_ = context.Archive.Append(binding.Workspace, binding.Channel, []archive.ArchivedMessage{record})
}

// relabel is the agent name to sign with: the forum name, not the MCP client key.
func relabel(context Context) string {
	if context.Identity != nil {
		return context.Identity.Name
	}

	return context.IdentityKey
}

// preview is what the channel will read, plus the envelope laid out for the person
// approving it. The document sits in a `markdown` fence on purpose: it is not what
// gets posted, and a `forum` fence here would read like the old wire format.
func preview(binding Binding, message Envelope, target, warning string) (WriteResult, error) {
	summary := SummaryFor(&message)

	if err := AssertFits(summary); err != nil {
		return WriteResult{}, err
	}

	body, err := message.Encode()
	if err != nil {
		return WriteResult{}, err
	}

	text := strings.Join([]string{
		summary,
		"",
		"It travels as " + envelope.FileName(message.ID) + " (" + strconv.Itoa(len(body)) +
			" bytes), and this line is all the channel shows.",
		"",
		"```markdown",
		body,
		"```",
	}, "\n")

	return WriteResult{
		Envelope: message,
		Binding:  binding,
		Text:     text,
		Target:   target,
		Warning:  warning,
	}, nil
}

// targetWarning says when a peer has been quiet for long enough to read as out:
// sending work to it is allowed, but pretending it is there would be a lie the
// requester's user pays for.
func targetWarning(context Context, target Target, roster []Peer, chosen *Peer) string {
	if chosen != nil {
		if MachineStatusOf(*chosen, float64(context.Limits.PeerTTLHours), context.Now) == MachineOut {
			hours := int((float64(context.Now.UnixMilli()) - numeric(chosen.TS)*1000) / 3_600_000)

			return chosen.From + " has not announced anything for " + strconv.Itoa(hours) +
				"h and reads as out; the task will wait for it"
		}

		return ""
	}

	if !target.Anyone {
		return ""
	}

	matching := Candidates(roster, target)
	if len(matching) == 0 {
		return ""
	}

	here := 0

	for _, peer := range matching {
		if HumanStatusOf(peer, context.Limits.HereSeconds, float64(context.Limits.PeerTTLHours), context.Now) == HumanHere {
			here++
		}
	}

	if here > 0 {
		return ""
	}

	return strconv.Itoa(len(matching)) + " peer(s) match, none here: the task is claimed when one of them looks"
}

// PostTaskArgs is what `post` is asked for.
type PostTaskArgs struct {
	To         string
	Title      string
	Body       string
	Capability string
	Deadline   string
	Parent     string
	Binding    string
	Confirm    bool
	By         string
}

// PostTask sends work to a peer, or previews it.
func PostTask(context Context, resolve TransportResolver, args PostTaskArgs, hook *RefreshHook) (WriteResult, error) {
	context, err := freshen(context, hook)
	if err != nil {
		return WriteResult{}, err
	}

	identity, err := context.RequireIdentity()
	if err != nil {
		return WriteResult{}, err
	}

	me := LabelOf(identity.Name, identity.Host)

	views := LoadViews(context, ViewFilter{})
	if len(views) == 0 {
		return WriteResult{}, failWrite("No forum channel bound on this machine.", "Bind one: `agent-forum add <channel>`.")
	}

	picked, err := pickBinding(views, args.To, args.Binding)
	if err != nil {
		return WriteResult{}, err
	}

	warning := ""
	if target, err := ParseTarget(args.To); err == nil {
		warning = targetWarning(context, target, picked.view.Roster, picked.peer)
	}

	message, err := taskEnvelope(context, picked, me, args)
	if err != nil {
		return WriteResult{}, err
	}

	if !args.Confirm {
		return preview(picked.view.Binding, message, picked.target, warning)
	}

	transport, err := resolve(picked.view.Binding.Workspace)
	if err != nil {
		return WriteResult{}, err
	}

	result, err := Deliver(context, transport, picked.view.Binding, message, "")
	if err != nil {
		return WriteResult{}, err
	}

	if _, err := context.Decision.Record(picked.view.Binding.Workspace, picked.view.Binding.Channel,
		DecisionInput{Task: message.ID, Decision: "posted", By: args.By}, context.Now); err != nil {
		return WriteResult{}, err
	}

	result.Target = picked.target
	result.Warning = joinNotes(warning, result.Warning)

	return result, nil
}

// taskEnvelope builds the envelope a post would carry, with its parent and deadline
// resolved here so a preview and a post cannot disagree.
func taskEnvelope(context Context, picked bindingChoice, me string, args PostTaskArgs) (Envelope, error) {
	depth := 0
	parentID := ""

	if args.Parent != "" {
		parent, err := LocateTask([]BindingView{picked.view}, args.Parent)
		if err != nil {
			return Envelope{}, err
		}

		parentID = parent.Task.ID
		depth = parent.Task.Depth + 1
	}

	message := Envelope{
		V: 1, Kind: KindTask, ID: NewID(context.Now),
		From: me, To: picked.toValue, Sent: context.Now.UTC().Format("2006-01-02T15:04:05.000Z"),
		Title: args.Title, Body: args.Body, HasBody: true,
		Capability: args.Capability, Parent: parentID,
	}

	if args.Deadline != "" {
		stamp, err := deadlineStamp(args.Deadline, context.Now)
		if err != nil {
			return Envelope{}, err
		}

		message.Deadline = stamp
	}

	if depth > 0 {
		message.Depth = &depth
	}

	return message, nil
}

// bindingChoice is a task's destination: which channel, and who in it.
type bindingChoice struct {
	view    BindingView
	toValue string
	target  string
	peer    *Peer
}

// pickBinding routes a task: a named peer picks the channel whose roster knows it; a
// broadcast needs exactly one channel or an explicit name. Guessing here would send
// work to the wrong channel and look exactly like a success.
func pickBinding(views []BindingView, to, only string) (bindingChoice, error) {
	if only != "" {
		return bindExplicit(views, to, only)
	}

	target, err := ParseTarget(to)
	if err != nil {
		return bindingChoice{}, err
	}

	if target.Anyone {
		return bindBroadcast(views)
	}

	return bindNamed(views, target)
}

func bindExplicit(views []BindingView, to, only string) (bindingChoice, error) {
	for _, view := range views {
		if view.Binding.Name != only && view.Key != only {
			continue
		}

		target, err := ParseTarget(to)
		if err != nil {
			return bindingChoice{}, err
		}

		if target.Anyone {
			return bindingChoice{view: view, toValue: "*", target: "*"}, nil
		}

		peer, err := ResolvePeer(view.Roster, target)
		if err != nil {
			return bindingChoice{}, err
		}

		return bindingChoice{view: view, toValue: peer.From, target: peer.From, peer: &peer}, nil
	}

	return bindingChoice{}, failWrite("No bound channel called \""+only+"\".", "`agent-forum list` shows the bindings.")
}

func bindBroadcast(views []BindingView) (bindingChoice, error) {
	if len(views) == 1 {
		return bindingChoice{view: views[0], toValue: "*", target: "*"}, nil
	}

	names := make([]string, 0, len(views))
	for _, view := range views {
		names = append(names, view.Binding.Name)
	}

	return bindingChoice{}, failWrite(
		"A broadcast needs one channel to go to, and "+strconv.Itoa(len(views))+" are bound.",
		"Pick one with --binding <name>: "+strings.Join(names, ", ")+".")
}

func bindNamed(views []BindingView, target Target) (bindingChoice, error) {
	knowing := make([]BindingView, 0, 2)

	for _, view := range views {
		if len(Candidates(view.Roster, target)) > 0 {
			knowing = append(knowing, view)
		}
	}

	switch len(knowing) {
	case 0:
		return bindingChoice{}, unknownPeer(views, target)
	case 1:
	default:
		names := make([]string, 0, len(knowing))
		for _, view := range knowing {
			names = append(names, "#"+view.Binding.Name)
		}

		return bindingChoice{}, failWrite("\""+TargetLabel(target)+"\" is known in "+
			strconv.Itoa(len(knowing))+" channels: "+strings.Join(names, ", ")+".", "Pick one with --binding <name>.")
	}

	peer, err := ResolvePeer(knowing[0].Roster, target)
	if err != nil {
		return bindingChoice{}, err
	}

	// A bare name is resolved here, so the envelope carries the exact agent: a reader
	// has only the roster to go on, and `to: reviewer` would match nobody.
	return bindingChoice{view: knowing[0], toValue: peer.From, target: peer.From, peer: &peer}, nil
}

func unknownPeer(views []BindingView, target Target) error {
	hint := "Bind one first: `agent-forum add <channel>` (or `agent-forum add #x`)."

	if len(views) > 0 {
		names := make([]string, 0, len(views))
		for _, view := range views {
			names = append(names, "#"+view.Binding.Name)
		}

		hint = "Run `agent-forum peers` to see who is around in " + strings.Join(names, ", ") + "."
	}

	return failWrite("No peer \""+TargetLabel(target)+"\" in any bound channel.", hint)
}

func joinNotes(parts ...string) string {
	said := make([]string, 0, len(parts))

	for _, part := range parts {
		if part != "" {
			said = append(said, part)
		}
	}

	return strings.Join(said, "; ")
}

var decisionOf = map[string]string{
	KindAccept:  "approved",
	KindDecline: "rejected",
	KindResult:  "reported",
	KindError:   "reported",
	KindAck:     "acknowledged",
}

// ReplyArgs is what a move on a task is asked for.
type ReplyArgs struct {
	ID      string
	Kind    string
	Body    string
	OK      *bool
	Confirm bool
	By      string
}

// ReplyTo is accept, decline, progress, result, error, ack, reopen and cancel.
func ReplyTo(context Context, resolve TransportResolver, args ReplyArgs, hook *RefreshHook) (WriteResult, error) {
	identity, err := context.RequireIdentity()
	if err != nil {
		return WriteResult{}, err
	}

	me := LabelOf(identity.Name, identity.Host)

	if needsBody(args.Kind) && args.Body == "" {
		if args.Kind == KindReopen {
			return WriteResult{}, failWrite("A reopen needs a body: say what is missing.", "")
		}

		return WriteResult{}, failWrite("A "+args.Kind+" needs a body: say what happened.", "")
	}

	resolveTask := func(current Context) (Located, error) {
		views := LoadViews(current, ViewFilter{})

		located, err := LocateTask(views, args.ID)
		if err != nil {
			return Located{}, err
		}

		return located, validateMove(current, located.Task, args)
	}

	context, located, err := locateWithRetry(context, hook, resolveTask)
	if err != nil {
		return WriteResult{}, err
	}

	message := replyEnvelope(context, me, located.Task.ID, args)

	if !args.Confirm {
		return preview(located.View.Binding, message, located.Task.Title, "")
	}

	transport, err := resolve(located.View.Binding.Workspace)
	if err != nil {
		return WriteResult{}, err
	}

	result, err := Deliver(context, transport, located.View.Binding, message, located.Task.TS)
	if err != nil {
		return WriteResult{}, err
	}

	if decision, decided := decisionOf[args.Kind]; decided {
		note := args.Body
		if len(note) > 200 {
			note = note[:200]
		}

		if _, err := context.Decision.Record(located.View.Binding.Workspace, located.View.Binding.Channel,
			DecisionInput{Task: located.Task.ID, Decision: decision, By: args.By, Note: note}, context.Now); err != nil {
			return WriteResult{}, err
		}
	}

	result.Target = located.Task.Title

	return result, nil
}

// replyEnvelope builds the envelope a move would post.
func replyEnvelope(context Context, me, taskID string, args ReplyArgs) Envelope {
	message := Envelope{
		V: 1, Kind: args.Kind, ID: NewID(context.Now), From: me,
		Sent: context.Now.UTC().Format("2006-01-02T15:04:05.000Z"), Task: taskID,
		Body: args.Body, HasBody: args.Body != "",
	}

	if args.Kind == KindResult {
		ok := true
		if args.OK != nil {
			ok = *args.OK
		}

		message.OK = &ok
	}

	return message
}

// locateWithRetry finds the task, and tries once more after a pause: Slack serves a
// message a second or two after accepting it, so a write can act on a view older than
// the thread. A real state error fails twice and is reported as it is.
func locateWithRetry(
	context Context,
	hook *RefreshHook,
	resolveTask func(Context) (Located, error),
) (Context, Located, error) {
	context, err := freshen(context, hook)
	if err != nil {
		return context, Located{}, err
	}

	located, err := resolveTask(context)
	if err == nil {
		return context, located, nil
	}

	if hook == nil || hook.Refresh == nil {
		return context, Located{}, err
	}

	delay := hook.RetryDelay
	if delay == 0 {
		delay = retryDelay
	}

	time.Sleep(delay)

	context, err = freshen(context, hook)
	if err != nil {
		return context, Located{}, err
	}

	located, err = resolveTask(context)
	if err != nil {
		return context, Located{}, err
	}

	return context, located, nil
}

func needsBody(kind string) bool {
	switch kind {
	case KindProgress, KindResult, KindError, KindReopen:
		return true
	default:
		return false
	}
}

// validateMove is what each move is allowed to say, before anything is built.
func validateMove(context Context, task Task, args ReplyArgs) error {
	switch args.Kind {
	case KindAccept:
		return requireOpen(task, context.MeLabel())
	case KindDecline:
		return validateDecline(context, task)
	case KindProgress:
		return validateProgress(context, task)
	case KindResult, KindError:
		return requireAccepted(task, context.MeLabel())
	case KindAck, KindReopen:
		return validateClose(context, task, args.Kind)
	case KindCancel:
		return validateCancel(context, task)
	default:
		return nil
	}
}

func validateDecline(context Context, task Task) error {
	if err := requireOpen(task, context.MeLabel()); err != nil {
		return err
	}

	if task.To == "*" {
		return failWrite("Task "+task.ID+" is a broadcast, and nobody has to answer it.",
			"Declining would refuse it on behalf of every other agent: ignore it instead.")
	}

	return nil
}

func validateProgress(context Context, task Task) error {
	me := context.MeLabel()

	if task.State != StateAccepted {
		return failWrite("Task "+task.ID+" is "+StateLabel(task, context.Now)+"; a note needs an accepted task.",
			"Claim it first, or wait for whoever was asked to claim it.")
	}

	if !taskAddressee(task, me) && task.From != me {
		return failWrite("Task "+task.ID+" is between "+task.From+" and "+task.To+".", "")
	}

	return nil
}

func validateClose(context Context, task Task, kind string) error {
	if task.State != StateDone && task.State != StateFailed {
		action := "acknowledged"
		if kind == KindReopen {
			action = "reopened"
		}

		return failWrite("Task "+task.ID+" is "+StateLabel(task, context.Now)+
			"; only a reported task can be "+action+".", "")
	}

	return requireRequester(task, context.MeLabel())
}

func validateCancel(context Context, task Task) error {
	if task.State != StateOpen && task.State != StateAccepted {
		return failWrite("Task "+task.ID+" is "+string(task.State)+"; nothing to cancel.", "")
	}

	return requireRequester(task, context.MeLabel())
}

// BindArgs is what `add` is asked for.
type BindArgs struct {
	Reference  string
	Backfill   bool
	Signed     bool
	NoPresence bool
}

// BindChannel records a channel this machine talks on.
func BindChannel(context Context, transport Transport, args BindArgs) (Binding, bool, error) {
	channel, err := transport.Client.ResolveChannel(args.Reference, nil)
	if err != nil {
		return Binding{}, false, err
	}

	mine, err := transport.Client.ListMyChannels()
	if err != nil {
		return Binding{}, false, err
	}

	member := false

	for _, entry := range mine {
		if entry.ID == channel.ID {
			member = true

			break
		}
	}

	if !member {
		return Binding{}, false, failWrite(describeChannel(channel)+" is not a channel this session can read.",
			"Bind a channel you are a member of; private channels are fine as long as you are in them.")
	}

	since := "0"

	if !args.Backfill {
		page, err := transport.Client.History(slack.HistoryParams{Channel: channel.ID, Limit: 1})
		if err != nil {
			return Binding{}, false, err
		}

		if len(page.Messages) > 0 {
			since = page.Messages[0].TS
		}
	}

	name := channel.Name
	if name == "" {
		name = channel.ID
	}

	binding := Binding{
		Workspace: transport.Account.TeamID,
		Channel:   channel.ID,
		Name:      name,
		Private:   channel.IsPrivate,
		SinceTS:   since,
		AddedAt:   context.Now.UTC().Format("2006-01-02T15:04:05.000Z"),
		Sign:      args.Signed,
		Announce:  !args.NoPresence,
	}

	added, err := context.Store.Bind(binding)

	return binding, added, err
}

func describeChannel(channel slack.Channel) string {
	if channel.Name != "" {
		return "#" + channel.Name
	}

	return channel.ID
}

// RegisterIdentity files a name, and answers whether the channel has to hear about
// it: a re-register that changes nothing is not announced twice.
func RegisterIdentity(context Context, identity Identity) (bool, error) {
	registered := StoredIdentity{
		Name:         identity.Name,
		Host:         identity.Host,
		Capabilities: identity.Capabilities,
		RegisteredAt: context.Now.UTC().Format("2006-01-02T15:04:05.000Z"),
	}

	if _, err := context.Store.Register(context.IdentityKey, registered); err != nil {
		return false, err
	}

	announced, err := context.Reload()
	if err != nil {
		return false, err
	}

	label := LabelOf(identity.Name, identity.Host)

	for _, view := range LoadViews(announced, ViewFilter{}) {
		for _, peer := range view.Roster {
			if peer.From == label && SameCapabilities(peer.Capabilities, identity.Capabilities) {
				return false, nil
			}
		}
	}

	return true, nil
}

// PresenceEnvelope is what a session says about the person in front of it.
func PresenceEnvelope(context Context, identity Identity, state, note string) Envelope {
	return Envelope{
		V: 1, Kind: KindPresence, ID: NewID(context.Now),
		From: LabelOf(identity.Name, identity.Host),
		Sent: context.Now.UTC().Format("2006-01-02T15:04:05.000Z"),
		Name: identity.Name, Host: identity.Host, State: state, Note: note,
	}
}

// BeaconEnvelope is the one envelope a background poll may post: this machine is up,
// nothing more.
func BeaconEnvelope(context Context, identity Identity) Envelope {
	return Envelope{
		V: 1, Kind: KindBeacon, ID: NewID(context.Now),
		From: LabelOf(identity.Name, identity.Host),
		Sent: context.Now.UTC().Format("2006-01-02T15:04:05.000Z"),
		Name: identity.Name, Host: identity.Host,
	}
}

// AnnounceEnvelope is what `register` posts.
func AnnounceEnvelope(context Context, identity Identity) Envelope {
	return Envelope{
		V: 1, Kind: KindRegister, ID: NewID(context.Now),
		From: LabelOf(identity.Name, identity.Host),
		Sent: context.Now.UTC().Format("2006-01-02T15:04:05.000Z"),
		Name: identity.Name, Host: identity.Host,
		Capabilities: identity.Capabilities, Version: buildVersion,
	}
}

// buildVersion is stamped by the caller, so the envelope names the build that posted
// it without this package importing the binary's build info.
var buildVersion = ""

// SetVersion tells the forum which build it is running as.
func SetVersion(version string) {
	buildVersion = version
}

// AnnounceResult is how many channels heard something, and how many stayed quiet.
type AnnounceResult struct {
	Posted  int
	Skipped int
}

// AnnounceLiveness is the one door for liveness, with the caller's path in the
// signature: `poll` is the background path and can only produce a beacon; `session`
// speaks for the person in front of it and never runs from a timer.
func AnnounceLiveness(
	context Context,
	resolve TransportResolver,
	path string,
	state, note string,
	force bool,
) (AnnounceResult, error) {
	if PresenceDisabled(context.Env) {
		return AnnounceResult{}, nil
	}

	if path == "poll" {
		return announceBeacon(context, resolve, force)
	}

	return announcePresence(context, resolve, state, note, force)
}

func announcePresence(context Context, resolve TransportResolver, state, note string, force bool) (AnnounceResult, error) {
	identity := context.Identity
	if identity == nil {
		return AnnounceResult{}, nil
	}

	if state == "" {
		state = string(HumanHere)
	}

	label := LabelOf(identity.Name, identity.Host)

	if !force {
		claim, known := LastClaim(context.Archive.Root, label)
		if known && claim.State == state &&
			context.Now.UnixMilli()-claim.At < int64(context.Limits.HereSeconds)*1000 {
			return AnnounceResult{}, nil
		}
	}

	result, err := announceInEvery(context, resolve, func(BindingView) Envelope {
		return PresenceEnvelope(context, identityOf(*identity), state, note)
	})
	if err != nil {
		return result, err
	}

	if result.Posted > 0 {
		_ = MarkClaim(context.Archive.Root, label, state, context.Now)
	}

	return result, nil
}

func announceBeacon(context Context, resolve TransportResolver, force bool) (AnnounceResult, error) {
	identity := context.Identity
	if identity == nil {
		return AnnounceResult{}, nil
	}

	label := LabelOf(identity.Name, identity.Host)

	if !force {
		if marked, known := LastBeacon(context.Archive.Root, label); known &&
			context.Now.UnixMilli()-marked < BeaconThrottleMS(context.Limits.PresenceSeconds) {
			return AnnounceResult{}, nil
		}
	}

	result, err := announceInEvery(context, resolve, func(BindingView) Envelope {
		return BeaconEnvelope(context, identityOf(*identity))
	})
	if err != nil {
		return result, err
	}

	if result.Posted > 0 {
		_ = MarkBeacon(context.Archive.Root, label, context.Now)
	}

	return result, nil
}

// announceInEvery posts one envelope to every binding that announces itself.
func announceInEvery(
	context Context,
	resolve TransportResolver,
	envelopeFor func(BindingView) Envelope,
) (AnnounceResult, error) {
	result := AnnounceResult{}

	for _, view := range LoadViews(context, ViewFilter{}) {
		if !view.Binding.Announce {
			result.Skipped++

			continue
		}

		transport, err := resolve(view.Binding.Workspace)
		if err != nil {
			return result, err
		}

		if _, err := Deliver(context, transport, view.Binding, envelopeFor(view), ""); err != nil {
			return result, err
		}

		result.Posted++
	}

	return result, nil
}

// UnregisterIdentity says goodbye, then forgets the name and retires the label so
// its inbox stops filling with work it will never do.
func UnregisterIdentity(context Context, resolve TransportResolver, announceIt bool) (string, int, error) {
	identity, err := context.RequireIdentity()
	if err != nil {
		return "", 0, err
	}

	label := LabelOf(identity.Name, identity.Host)
	posted := 0

	if announceIt {
		for _, view := range LoadViews(context, ViewFilter{}) {
			transport, err := resolve(view.Binding.Workspace)
			if err != nil {
				return label, posted, err
			}

			if _, err := Deliver(context, transport, view.Binding,
				PresenceEnvelope(context, identityOf(identity), "gone", ""), ""); err != nil {
				return label, posted, err
			}

			posted++
		}
	}

	_, err = context.Store.Unregister(context.IdentityKey, label)

	return label, posted, err
}

// Announce tells every channel that does not know this name yet that it exists.
func Announce(context Context, resolve TransportResolver, identity Identity) (AnnounceResult, error) {
	message := AnnounceEnvelope(context, identity)
	result := AnnounceResult{}

	for _, view := range LoadViews(context, ViewFilter{}) {
		if !view.Binding.Announce {
			result.Skipped++

			continue
		}

		known := false

		for _, peer := range view.Roster {
			if peer.From == message.From && SameCapabilities(peer.Capabilities, identity.Capabilities) {
				known = true

				break
			}
		}

		if known {
			result.Skipped++

			continue
		}

		transport, err := resolve(view.Binding.Workspace)
		if err != nil {
			return result, err
		}

		if _, err := Deliver(context, transport, view.Binding, message, ""); err != nil {
			return result, err
		}

		result.Posted++
	}

	return result, nil
}

// identityOf lifts a stored identity to the shape the envelopes use.
func identityOf(stored StoredIdentity) Identity {
	return Identity{Name: stored.Name, Host: stored.Host, Capabilities: stored.Capabilities}
}
