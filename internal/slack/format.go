package slack

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MaxMessageChars is Slack's own recommendation per message. The archive holds
// longer ones, so a channel read cuts there and says so instead of quietly
// handing back half a message.
const MaxMessageChars = 4000

var (
	userIDPattern = regexp.MustCompile(`^[UW][A-Z0-9]{5,}$`)
	groupDMName   = regexp.MustCompile(`^mpdm-(.+?)(?:-\d+)?$`)
)

// FormatTS is the `ts` as a person reads a time, keeping the raw value elsewhere
// so a follow-up call can address the same message.
func FormatTS(ts string) string {
	seconds, _, _ := strings.Cut(ts, ".")

	parsed, err := strconv.ParseInt(seconds, 10, 64)
	if err != nil {
		return ts
	}

	return time.Unix(parsed, 0).UTC().Format("2006-01-02 15:04:05")
}

func groupDMHandles(name string) []string {
	match := groupDMName.FindStringSubmatch(name)
	if match == nil {
		return nil
	}

	handles := make([]string, 0, 4)

	for handle := range strings.SplitSeq(match[1], "--") {
		if handle != "" {
			handles = append(handles, handle)
		}
	}

	return handles
}

// fileLabel keeps the id next to the name: a re-uploaded or renamed file keeps
// its id, the label does not.
func fileLabel(file File) string {
	label := file.Name
	if label == "" {
		label = file.Title
	}

	if label == "" {
		label = file.Mimetype
	}

	if label == "" {
		label = "unnamed"
	}

	if file.ID != "" {
		return "[file: " + label + " id=" + file.ID + "]"
	}

	return "[file: " + label + "]"
}

func messageBody(message Message, scope *ReferenceScope) string {
	text := strings.TrimSpace(message.Text)

	if text == "" && message.Subtype != "" {
		text = "(" + message.Subtype + ")"
	}

	text = ResolveReferences(text, scope)

	for _, file := range message.Files {
		if text != "" {
			text += " "
		}

		text += fileLabel(file)
	}

	if len([]rune(text)) > MaxMessageChars {
		total := len([]rune(text))
		text = string([]rune(text)[:MaxMessageChars]) +
			fmt.Sprintf("… [truncated: %d characters, search_messages prints a message whole]", total)
	}

	return text
}

func indentContinuation(text, prefix string) string {
	return strings.ReplaceAll(text, "\n", "\n"+prefix)
}

// RenderOptions is what rendering needs beyond the messages: who wrote them and
// what the ids in them are called.
type RenderOptions struct {
	Names map[string]string
	Scope *ReferenceScope
}

// RenderMessages is one line per message, oldest first as the caller orders it.
func RenderMessages(messages []Message, options RenderOptions) string {
	lines := make([]string, 0, len(messages))

	for _, message := range messages {
		name := message.User

		if name != "" {
			if resolved, ok := options.Names[message.User]; ok {
				name = resolved
			}
		} else {
			switch {
			case message.Username != "":
				name = message.Username
			case message.BotID != "":
				name = message.BotID
			default:
				name = "unknown"
			}
		}

		body := indentContinuation(messageBody(message, options.Scope), "    ")

		thread := ""
		if message.ReplyCount > 0 && message.ThreadTS == "" {
			thread = fmt.Sprintf("  (%d replies, thread_ts %s)", message.ReplyCount, message.TS)
		}

		lines = append(lines, "["+message.TS+"] "+FormatTS(message.TS)+" "+name+": "+body+thread)
	}

	return strings.Join(lines, "\n")
}
