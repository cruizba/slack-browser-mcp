package forum

import (
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cruizba/slack-browser-mcp/internal/failure"
	"github.com/cruizba/slack-browser-mcp/internal/slack"
)

// The wire format: a human summary in the message text and the envelope itself as a
// `forum-<id>.md` attachment, frontmatter for the fields and markdown for the body.
//
// Two older shapes are still decoded because the channels hold them and the ledger
// rebuilds itself from them: a fenced `forum` block in the text (up to v0.2.1) and a
// `forum-<id>.json` file (up to v0.3.2). Nothing writes either any more.
const (
	// ForumFence is the marker of the shape up to v0.2.1.
	ForumFence = "forum"
	// MaxBodyBytes is what a body may weigh once it travels as a file: a whole
	// transcript, not a paste.
	MaxBodyBytes = 1024 * 1024
	// MaxTitleChars is the title a person reads in the summary line.
	MaxTitleChars = 120
	// MaxFromChars and the rest bound the identifiers that end up in one line.
	MaxFromChars     = 64
	MaxTargetChars   = 64
	MaxNameChars     = 32
	MaxTagChars      = 24
	MaxDeadlineChars = 24
	// MaxTextChars is where Slack refuses a message rather than truncating it.
	MaxTextChars = 4000
)

// The kinds of envelope. The four announcements carry who is there; the rest are
// replies to a task.
const (
	KindRegister = "register"
	KindBeacon   = "beacon"
	KindPresence = "presence"
	KindTask     = "task"
	KindAccept   = "accept"
	KindDecline  = "decline"
	KindProgress = "progress"
	KindResult   = "result"
	KindError    = "error"
	KindAck      = "ack"
	KindReopen   = "reopen"
	KindCancel   = "cancel"
)

// Slack flattens `text` to a single line when the message carries blocks, so a
// line-anchored fence stops matching on the very messages this tool posts. The fence
// is found by its delimiters and its braces instead.
var fencePattern = regexp.MustCompile("(?s)```forum[ \t\r\n]*(\\{.*?\\})[ \t\r\n]*```")

// Envelope is one message of the forum, whatever kind it is. Fields a kind does not
// use stay zero, and Encode refuses an envelope whose fields do not fit its kind.
type Envelope struct {
	V            int
	Kind         string
	ID           string
	From         string
	To           string
	Name         string
	Host         string
	State        string
	Sent         string
	Title        string
	Tag          string
	Capability   string
	Deadline     string
	Task         string
	Parent       string
	Depth        *int
	OK           *bool
	Capabilities []string
	Note         string
	Version      string
	Body         string
	// HasBody distinguishes a reply that carries no body from one that carries an
	// empty one, which the schema refuses.
	HasBody bool
}

// fieldOrder is the order a person reading the file sees, independent of the order
// the struct happens to declare: what it is, who sent it, what it says.
var fieldOrder = []string{
	"v", "kind", "id", "from", "to", "name", "host", "state", "sent", "title",
	"tag", "deadline", "task", "parent", "depth", "ok", "capabilities", "note", "version",
}

// Validate applies the rules of the envelope's kind. Everything here is a rule a
// reader could rely on, so a document that fails is not an envelope at all.
func (e *Envelope) Validate() error {
	if e.V != 1 {
		return errors.New("v must be 1")
	}

	if !IsID(e.ID) {
		return errors.New("id is not a forum id")
	}

	e.From = strings.TrimSpace(e.From)
	if problem := lengthProblem("from", e.From, 1, MaxFromChars); problem != "" {
		return errors.New(problem)
	}

	e.Sent = strings.TrimSpace(e.Sent)
	if len(e.Sent) < 10 || len(e.Sent) > MaxDeadlineChars {
		return errors.New("sent must be a timestamp")
	}

	switch e.Kind {
	case KindRegister, KindBeacon, KindPresence:
		return e.validateAnnouncement()
	case KindTask:
		return e.validateTask()
	case KindAccept, KindDecline, KindProgress, KindResult, KindError, KindAck, KindReopen, KindCancel:
		return e.validateReply()
	default:
		return errors.New("unknown kind " + e.Kind)
	}
}

func (e *Envelope) validateAnnouncement() error {
	e.Name = strings.TrimSpace(e.Name)
	e.Host = strings.TrimSpace(e.Host)

	if problem := lengthProblem("name", e.Name, 1, MaxNameChars); problem != "" {
		return errors.New(problem)
	}

	if problem := lengthProblem("host", e.Host, 1, MaxNameChars); problem != "" {
		return errors.New(problem)
	}

	if e.From != e.Name+"@"+e.Host {
		return errors.New("from must be name@host")
	}

	switch e.Kind {
	case KindRegister:
		tags := make([]string, 0, len(e.Capabilities))

		for _, tag := range e.Capabilities {
			trimmed := strings.TrimSpace(tag)
			if problem := lengthProblem("capabilities", trimmed, 1, MaxTagChars); problem != "" {
				return errors.New(problem)
			}

			tags = append(tags, trimmed)
		}

		if len(tags) > MaxCapabilities {
			return errors.New("at most 8 capabilities")
		}

		e.Capabilities = tags

		if e.Version != "" {
			e.Version = strings.TrimSpace(e.Version)

			if len(e.Version) > 16 {
				return errors.New("version is too long")
			}
		}
	case KindPresence:
		switch e.State {
		case string(HumanHere), string(HumanAway), "gone", "listening", "idle":
		default:
			return errors.New("state is not a presence")
		}

		e.Note = strings.TrimSpace(e.Note)

		if len(e.Note) > 80 {
			return errors.New("note is too long")
		}
	}

	return nil
}

func (e *Envelope) validateTask() error {
	e.To = strings.TrimSpace(e.To)
	if problem := lengthProblem("to", e.To, 1, MaxTargetChars); problem != "" {
		return errors.New(problem)
	}

	e.Title = strings.TrimSpace(e.Title)
	if problem := lengthProblem("title", e.Title, 1, MaxTitleChars); problem != "" {
		return errors.New(problem)
	}

	if problem := e.validateBody(true); problem != "" {
		return errors.New(problem)
	}

	if e.Capability != "" {
		e.Capability = strings.TrimSpace(e.Capability)

		if problem := lengthProblem("capability", e.Capability, 1, MaxTagChars); problem != "" {
			return errors.New(problem)
		}
	}

	if e.Tag != "" {
		e.Tag = strings.TrimSpace(e.Tag)

		if problem := lengthProblem("tag", e.Tag, 1, MaxTagChars); problem != "" {
			return errors.New(problem)
		}

		if e.To != "*" {
			return errors.New("tag only narrows a broadcast (to: \"*\")")
		}
	}

	return e.validateOptionalBookkeeping()
}

func (e *Envelope) validateReply() error {
	if !IsID(e.Task) {
		return errors.New("task is not a forum id")
	}

	required := e.Kind != KindAccept && e.Kind != KindDecline && e.Kind != KindAck && e.Kind != KindCancel

	if problem := e.validateBody(required); problem != "" {
		return errors.New(problem)
	}

	if e.Kind == KindResult && e.OK == nil {
		ok := true
		e.OK = &ok
	}

	return e.validateOptionalBookkeeping()
}

func (e *Envelope) validateOptionalBookkeeping() error {
	if e.Deadline != "" {
		e.Deadline = strings.TrimSpace(e.Deadline)

		if len(e.Deadline) < 10 || len(e.Deadline) > MaxDeadlineChars {
			return errors.New("deadline must be a timestamp")
		}
	}

	if e.Parent != "" && !IsID(e.Parent) {
		return errors.New("parent is not a forum id")
	}

	if e.Depth != nil && (*e.Depth < 0 || *e.Depth > 9) {
		return errors.New("depth must be between 0 and 9")
	}

	return nil
}

func (e *Envelope) validateBody(required bool) string {
	if !e.HasBody {
		if required {
			return "body is required"
		}

		return ""
	}

	e.Body = strings.TrimSpace(e.Body)

	if e.Body == "" {
		if required {
			return "body is required"
		}

		e.HasBody = false

		return ""
	}

	if len(e.Body) > MaxBodyBytes {
		return "a body carries at most " + strconv.Itoa(MaxBodyBytes) +
			" bytes, which is a whole thread; link to the file instead of pasting more"
	}

	return ""
}

func lengthProblem(field, value string, minimum, maximum int) string {
	size := utf8.RuneCountInString(value)

	switch {
	case size < minimum:
		return field + " is too short"
	case size > maximum:
		return field + " is longer than " + strconv.Itoa(maximum) + " characters"
	default:
		return ""
	}
}

// Encode renders the attachment: fields in frontmatter, body as markdown below it.
func (e *Envelope) Encode() (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}

	fields := e.fieldList()

	var body *string

	if e.HasBody {
		body = &e.Body
	}

	return WriteFrontmatter(fields, body)
}

// fieldList is the envelope as ordered fields, which is what both encoders read.
func (e *Envelope) fieldList() []Field {
	fields := make([]Field, 0, len(fieldOrder))

	add := func(key string, value any, ok bool) {
		if ok {
			fields = append(fields, Field{Key: key, Value: value})
		}
	}

	add("v", e.V, true)
	add("kind", e.Kind, true)
	add("id", e.ID, true)
	add("from", e.From, true)
	add("to", e.To, e.To != "")
	add("name", e.Name, e.Name != "")
	add("host", e.Host, e.Host != "")
	add("state", e.State, e.State != "")
	add("sent", e.Sent, true)
	add("title", e.Title, e.Title != "")
	add("tag", e.Tag, e.Tag != "")
	add("deadline", e.Deadline, e.Deadline != "")
	add("task", e.Task, e.Task != "")
	add("parent", e.Parent, e.Parent != "")

	if e.Depth != nil {
		add("depth", *e.Depth, true)
	}

	if e.OK != nil {
		add("ok", *e.OK, true)
	}

	add("capabilities", e.Capabilities, e.Capabilities != nil)
	add("note", e.Note, e.Note != "")
	add("version", e.Version, e.Version != "")

	return fields
}

// EncodeJSON is the legacy writer's shape, which the fixtures still use.
func (e *Envelope) EncodeJSON() (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}

	encoded, err := json.Marshal(e.fields())
	if err != nil {
		return "", err
	}

	return string(encoded), nil
}

func (e *Envelope) fields() map[string]any {
	out := make(map[string]any, len(fieldOrder))

	for _, field := range e.fieldList() {
		out[field.Key] = field.Value
	}

	if e.HasBody {
		out["body"] = e.Body
	}

	return out
}

// DecodeMarkdown reads the current shape: frontmatter over a markdown body.
func DecodeMarkdown(text string) *Envelope {
	document := ReadFrontmatter(text)
	if document == nil {
		return nil
	}

	values := document.Values

	if document.Body != "" {
		values["body"] = document.Body
	}

	return parseEnvelope(values)
}

// DecodeJSON reads the `forum-<id>.json` shape. `legacy` asks for Slack's escaping
// to be undone, which only the fenced reader needs.
func DecodeJSON(raw string, legacy bool) *Envelope {
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}

	if legacy {
		parsed = unescapeForm(parsed)
	}

	values, ok := parsed.(map[string]any)
	if !ok {
		return nil
	}

	return parseEnvelope(values)
}

// unescapeForm undoes Slack's escaping inside the values a fence carried. It runs
// after JSON parsing, never before: the quotes and braces that make the JSON are the
// sender's, and only the values travelled through Slack.
func unescapeForm(value any) any {
	switch typed := value.(type) {
	case string:
		return slack.UnescapeEntities(typed)
	case []any:
		out := make([]any, len(typed))
		for index, entry := range typed {
			out[index] = unescapeForm(entry)
		}

		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, entry := range typed {
			out[key] = unescapeForm(entry)
		}

		return out
	default:
		return value
	}
}

// DecodeFence reads the shape written up to v0.2.1.
func DecodeFence(text string) *Envelope {
	match := fencePattern.FindStringSubmatch(text)
	if match == nil {
		return nil
	}

	return DecodeJSON(strings.TrimSpace(match[1]), true)
}

func parseEnvelope(values map[string]any) *Envelope {
	envelope := &Envelope{}

	envelope.V = int(numberOf(values["v"]))
	envelope.Kind = stringOf(values["kind"])
	envelope.ID = stringOf(values["id"])
	envelope.From = stringOf(values["from"])
	envelope.To = stringOf(values["to"])
	envelope.Name = stringOf(values["name"])
	envelope.Host = stringOf(values["host"])
	envelope.State = stringOf(values["state"])
	envelope.Sent = stringOf(values["sent"])
	envelope.Title = stringOf(values["title"])
	envelope.Tag = stringOf(values["tag"])
	envelope.Capability = stringOf(values["capability"])
	envelope.Deadline = stringOf(values["deadline"])
	envelope.Task = stringOf(values["task"])
	envelope.Parent = stringOf(values["parent"])
	envelope.Note = stringOf(values["note"])
	envelope.Version = stringOf(values["version"])

	if depth, ok := values["depth"].(float64); ok {
		value := int(depth)
		envelope.Depth = &value
	}

	if ok, isBool := values["ok"].(bool); isBool {
		envelope.OK = &ok
	}

	if list, isList := values["capabilities"].([]any); isList {
		tags := make([]string, 0, len(list))

		for _, entry := range list {
			if tag, isString := entry.(string); isString {
				tags = append(tags, tag)
			} else {
				return nil
			}
		}

		envelope.Capabilities = tags
	} else if _, present := values["capabilities"]; present {
		return nil
	}

	if body, isString := values["body"].(string); isString && body != "" {
		envelope.Body = body
		envelope.HasBody = true
	}

	if err := envelope.Validate(); err != nil {
		return nil
	}

	return envelope
}

func stringOf(value any) string {
	if text, ok := value.(string); ok {
		return text
	}

	return ""
}

func numberOf(value any) float64 {
	if number, ok := value.(float64); ok {
		return number
	}

	return 0
}

// IsReply reports whether the envelope answers a task rather than announcing
// something.
func IsReply(envelope *Envelope) bool {
	switch envelope.Kind {
	case KindTask, KindRegister, KindBeacon, KindPresence:
		return false
	default:
		return true
	}
}

func firstLine(body string, maxChars int) string {
	line, _, _ := strings.Cut(body, "\n")
	line = strings.TrimSpace(line)

	if utf8.RuneCountInString(line) > maxChars {
		return string([]rune(line)[:maxChars-1]) + "…"
	}

	return line
}

// SummaryFor is the human line above the envelope. For a task it says who is asking
// whom; for a reply it says what happened, because in a thread the reader already
// knows which task it is.
func SummaryFor(envelope *Envelope) string {
	switch envelope.Kind {
	case KindRegister:
		return envelope.From + " joined the forum"
	case KindBeacon:
		return envelope.From + " is up"
	case KindPresence:
		return presenceSummary(envelope)
	case KindTask:
		return taskSummary(envelope)
	default:
		return replySummary(envelope)
	}
}

func presenceSummary(envelope *Envelope) string {
	if envelope.State == "gone" {
		return envelope.From + " left the forum" + withNote(envelope.Note)
	}

	return envelope.From + " is " + envelope.State + withNote(envelope.Note)
}

func taskSummary(envelope *Envelope) string {
	capability := envelope.Capability
	if capability == "" {
		capability = envelope.Tag
	}

	if capability == "" {
		return envelope.From + " asks " + envelope.To + ": " + envelope.Title
	}

	if envelope.To == "*" {
		return envelope.From + " asks anyone who can " + capability + ": " + envelope.Title
	}

	return envelope.From + " asks " + envelope.To + " for " + capability + ": " + envelope.Title
}

func replySummary(envelope *Envelope) string {
	line := firstLine(envelope.Body, 80)

	switch envelope.Kind {
	case KindAccept:
		return envelope.From + " accepted " + envelope.Task
	case KindDecline:
		return envelope.From + " declined " + envelope.Task + withBody(envelope.Body)
	case KindProgress:
		return envelope.From + " on " + envelope.Task + ": " + line
	case KindResult:
		if envelope.OK != nil && !*envelope.OK {
			return envelope.From + " could not finish " + envelope.Task + ": " + line
		}

		return envelope.From + " finished " + envelope.Task + ": " + line
	case KindError:
		return envelope.From + " failed " + envelope.Task + ": " + line
	case KindAck:
		return envelope.From + " received " + envelope.Task
	case KindReopen:
		return envelope.From + " reopened " + envelope.Task + ": " + line
	case KindCancel:
		return envelope.From + " cancelled " + envelope.Task + withBody(envelope.Body)
	default:
		return envelope.From + " " + envelope.Kind
	}
}

func withNote(note string) string {
	if note == "" {
		return ""
	}

	return ": " + note
}

func withBody(body string) string {
	if body == "" {
		return ""
	}

	return ": " + firstLine(body, 80)
}

// AssertFits refuses a message Slack would cut. A work request with its tail missing
// is a wrong request, so the caller shortens it or points at the detail.
func AssertFits(text string) error {
	if len([]rune(text)) > MaxTextChars {
		return &failure.StoreError{Message: "The message is " + strconv.Itoa(len([]rune(text))) +
			" characters and Slack accepts " + strconv.Itoa(MaxTextChars) +
			", so the end would be cut off. Shorten the body or point at the detail instead of pasting it."}
	}

	return nil
}
