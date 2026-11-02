package forum

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

// Local lines: `forum say` reaches an agent connected to this machine without writing
// to Slack.
//
// They are spooled per peer instead of held in the daemon's memory, which is a
// deliberate difference from the TypeScript build: a line survives the daemon being
// restarted, and a test can read what was delivered without standing up a daemon. What
// stays the same is the rule that makes it a nudge rather than the work — the daemon
// has to be running, because nothing in Slack records what was said.
const sayDirName = "say"

// LocalMessage is one line waiting for the peer it names.
type LocalMessage struct {
	From string `json:"from"`
	Text string `json:"text"`
	At   string `json:"at"`
}

func sayPath(forumDir, label string) string {
	return filepath.Join(forumDir, sayDirName, markKey(label)+".jsonl")
}

// DeliverSay spools one line for a peer. It refuses when this machine has no daemon,
// because a local message is recorded nowhere and a line nobody can receive is a line
// that was never sent.
func DeliverSay(forumDir, socket, to, from, text string, timeout time.Duration) error {
	if !Answers(socket, timeout) {
		return errors.New("no forum daemon is running on this machine, so nothing can be delivered " +
			"locally; post to Slack instead")
	}

	if strings.TrimSpace(to) == "" || strings.TrimSpace(text) == "" {
		return errors.New("a local line needs a peer and something to say")
	}

	path := sayPath(forumDir, to)

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	if err := platform.HardenDir(filepath.Dir(path)); err != nil {
		return err
	}

	encoded, err := json.Marshal(LocalMessage{
		From: from,
		Text: text,
		At:   time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	})
	if err != nil {
		return err
	}

	handle, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // the path is the forum directory plus a label
	if err != nil {
		return err
	}

	if _, err := handle.Write(append(encoded, '\n')); err != nil {
		_ = handle.Close()

		return err
	}

	if err := handle.Close(); err != nil {
		return err
	}

	return platform.Harden(path)
}

// DrainSay answers the lines waiting for a peer and forgets them: a local line is a
// nudge, so reading it once is the whole contract.
func DrainSay(forumDir, label string) []LocalMessage {
	path := sayPath(forumDir, label)

	raw, err := os.ReadFile(path) //nolint:gosec // the path is the forum directory plus a label
	if err != nil {
		return nil
	}

	if err := os.Remove(path); err != nil {
		return nil
	}

	messages := make([]LocalMessage, 0, 4)

	for line := range strings.SplitSeq(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}

		var message LocalMessage
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			continue
		}

		messages = append(messages, message)
	}

	return messages
}
