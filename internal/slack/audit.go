package slack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/platform"
	"github.com/cruizba/slack-browser-mcp/internal/redact"
)

// previewChars is how much of a message the audit keeps. Sending is the only side
// effect this tool has on a shared surface, so every post leaves a line; the line
// holds a redacted preview, never the whole message.
const previewChars = 120

// SendRecord is one message that went out.
type SendRecord struct {
	Workspace string
	Channel   string
	ChannelID string
	ThreadTS  string
	// MessageTS is absent when Slack reported no ts for an upload, which is rare and
	// not worth a failure.
	MessageTS string
	Text      string
	// Files are names and sizes: the bytes stay where the caller left them.
	Files []string
}

// RemovalRecord is a delete or an edit. A removal is the one thing the audit has to
// remember in full: after Slack deletes the message there is nothing left to read,
// so a redacted preview is the only record that it existed.
type RemovalRecord struct {
	Action    string
	Workspace string
	Channel   string
	ChannelID string
	MessageTS string
	ThreadTS  string
	Text      string
}

// auditLine is the JSON shape both records share, in the order a person reads.
type auditLine struct {
	At        string   `json:"at"`
	Action    string   `json:"action,omitempty"`
	Workspace string   `json:"workspace"`
	Channel   string   `json:"channel"`
	ChannelID string   `json:"channel_id"`
	ThreadTS  string   `json:"thread_ts,omitempty"`
	MessageTS string   `json:"message_ts,omitempty"`
	Files     []string `json:"files,omitempty"`
	Chars     int      `json:"chars"`
	Preview   string   `json:"preview"`
}

// RecordSend appends one sent message to the audit log and answers where it went.
func RecordSend(file string, record SendRecord, when time.Time) (string, error) {
	return appendAudit(file, auditLine{
		At:        when.UTC().Format("2006-01-02T15:04:05.000Z"),
		Workspace: record.Workspace,
		Channel:   record.Channel,
		ChannelID: record.ChannelID,
		ThreadTS:  record.ThreadTS,
		MessageTS: record.MessageTS,
		Files:     record.Files,
		Chars:     len([]rune(record.Text)),
		Preview:   preview(record.Text),
	})
}

// RecordRemoval appends one deletion or edit.
func RecordRemoval(file string, record RemovalRecord, when time.Time) (string, error) {
	return appendAudit(file, auditLine{
		At:        when.UTC().Format("2006-01-02T15:04:05.000Z"),
		Action:    record.Action,
		Workspace: record.Workspace,
		Channel:   record.Channel,
		ChannelID: record.ChannelID,
		ThreadTS:  record.ThreadTS,
		MessageTS: record.MessageTS,
		Chars:     len([]rune(record.Text)),
		Preview:   preview(record.Text),
	})
}

func preview(text string) string {
	clean := []rune(redact.Text(text))
	if len(clean) > previewChars {
		clean = clean[:previewChars]
	}

	return string(clean)
}

func appendAudit(file string, line auditLine) (string, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return "", err
	}

	encoded, err := json.Marshal(line)
	if err != nil {
		return "", err
	}

	handle, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // the path is the audit log from config
	if err != nil {
		return "", err
	}

	if _, err := handle.Write(append(encoded, '\n')); err != nil {
		_ = handle.Close()

		return "", err
	}

	if err := handle.Close(); err != nil {
		return "", err
	}

	if err := platform.Harden(file); err != nil {
		return "", err
	}

	return file, nil
}
