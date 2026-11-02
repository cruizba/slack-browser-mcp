package forum

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

// Decision is what the user decided about a task on this machine, kept next to the
// channel instead of inside it. The channel already says what was posted; this file
// says who approved it, and the two can disagree — approved locally and never
// announced, or announced and never approved — which is the single most useful thing
// a human auditing a forum can see.
type Decision struct {
	At           string `json:"at"`
	Task         string `json:"task"`
	Decision     string `json:"decision"`
	By           string `json:"by"`
	SelfApproved bool   `json:"self_approved,omitempty"`
	Note         string `json:"note,omitempty"`
}

// DecisionInput is one decision to record.
type DecisionInput struct {
	Task     string
	Decision string
	By       string
	Note     string
}

// SelfApproved records that an agent, not a terminal, took the decision. It prevents
// nothing: it makes the pattern visible in the inbox and in the audit, which is all a
// local gate can honestly promise.
func SelfApproved(input DecisionInput) bool {
	return input.By == "agent"
}

// DecisionLog is the append-only file per channel, last line wins.
type DecisionLog struct {
	root string
}

// NewDecisionLog points at the forum directory.
func NewDecisionLog(root string) *DecisionLog {
	return &DecisionLog{root: root}
}

var decisionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// File is where one channel's decisions live.
func (l *DecisionLog) File(teamID, channelID string) (string, error) {
	if !decisionIDPattern.MatchString(teamID) {
		return "", errors.New("Invalid team id: " + teamID)
	}

	if !decisionIDPattern.MatchString(channelID) {
		return "", errors.New("Invalid channel id: " + channelID)
	}

	return filepath.Join(l.root, teamID, channelID+".jsonl"), nil
}

// Record appends one decision.
func (l *DecisionLog) Record(teamID, channelID string, input DecisionInput, when time.Time) (string, error) {
	file, err := l.File(teamID, channelID)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Join(l.root, teamID), 0o700); err != nil {
		return "", err
	}

	entry := Decision{
		At:       when.UTC().Format("2006-01-02T15:04:05.000Z"),
		Task:     input.Task,
		Decision: input.Decision,
		By:       input.By,
	}

	if input.By == "agent" {
		entry.SelfApproved = true
	}

	entry.Note = input.Note

	encoded, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}

	handle, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // the path is the decision log
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

// Read answers the last decision per task. A half-written final line is not worth
// failing a read over.
func (l *DecisionLog) Read(teamID, channelID string) (map[string]Decision, error) {
	file, err := l.File(teamID, channelID)
	if err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(file) //nolint:gosec // the path is validated ids
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]Decision{}, nil
		}

		return nil, err
	}

	byTask := map[string]Decision{}

	for _, line := range splitLines(string(raw)) {
		var parsed Decision
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			continue
		}

		if parsed.Task != "" && parsed.Decision != "" {
			byTask[parsed.Task] = parsed
		}
	}

	return byTask, nil
}

// splitLines answers the non-blank lines of a line-per-record file.
func splitLines(raw string) []string {
	lines := make([]string, 0, 16)

	for line := range strings.SplitSeq(raw, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}

	return lines
}
