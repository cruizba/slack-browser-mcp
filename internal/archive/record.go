package archive

import (
	"encoding/json"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/slack"
)

// ChannelName is what the archive files as the conversation's name: a channel's
// name, or the peer's user id for a DM, which is all Slack says about it.
func ChannelName(channel slack.Channel) string {
	if channel.Name != "" {
		return channel.Name
	}

	return channel.User
}

// ToRecord shapes a message the way the archive keeps it.
func ToRecord(channel slack.Channel, message slack.Message, now time.Time, source string) ArchivedMessage {
	record := ArchivedMessage{
		TS:          message.TS,
		Chan:        channel.ID,
		ChannelName: ChannelName(channel),
		User:        message.User,
		Username:    message.Username,
		BotID:       message.BotID,
		ThreadTS:    message.ThreadTS,
		Subtype:     message.Subtype,
		Source:      source,
		SyncedAt:    now.UTC().Format("2006-01-02T15:04:05.000Z"),
	}

	text := message.Text
	record.Text = &text

	if message.Edited != nil && message.Edited.TS != "" {
		record.EditedTS = message.Edited.TS
	}

	record.ReplyCount = message.ReplyCount

	for _, file := range message.Files {
		archived := ArchivedFile{
			ID:       file.ID,
			Name:     file.Name,
			Title:    file.Title,
			Mimetype: file.Mimetype,
			Size:     file.Size,
		}

		if archived == (ArchivedFile{}) {
			continue
		}

		record.Files = append(record.Files, archived)
	}

	return record
}

// Changed reports whether Slack's copy differs from ours, which is what decides
// whether a sync appends anything at all.
func Changed(cached *ArchivedMessage, record ArchivedMessage) bool {
	if cached == nil {
		return true
	}

	if cached.DeletedAt != "" {
		return true
	}

	// A message filed from search becomes real history once a channel fetch sees
	// it, so the origin is part of what makes a record stale.
	if cached.Source != record.Source ||
		cached.Body() != record.Body() ||
		cached.EditedTS != record.EditedTS ||
		!sameNumber(cached.ReplyCount, record.ReplyCount) {
		return true
	}

	// A rename leaves every archived line with the old name, and the archive filters
	// by the current one. Only when the fresh copy carries a name.
	if record.ChannelName != "" && cached.ChannelName != record.ChannelName {
		return true
	}

	// Only when the fresh copy carries one: a sync that could not download must not
	// erase the envelope a previous run stored.
	if record.Forum != "" && cached.Forum != record.Forum {
		return true
	}

	return !sameFiles(cached.Files, record.Files)
}

func sameNumber(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}

	return *left == *right
}

func sameFiles(left, right []ArchivedFile) bool {
	if len(left) == 0 && len(right) == 0 {
		return true
	}

	encodedLeft, errLeft := json.Marshal(left)
	encodedRight, errRight := json.Marshal(right)

	if errLeft != nil || errRight != nil {
		return false
	}

	return string(encodedLeft) == string(encodedRight)
}

// FromRecord is the inverse: what the archive holds, shaped the way the API would
// have said it.
func FromRecord(record ArchivedMessage) slack.Message {
	message := slack.Message{
		TS:       record.TS,
		User:     record.User,
		Username: record.Username,
		BotID:    record.BotID,
		ThreadTS: record.ThreadTS,
		Subtype:  record.Subtype,
	}

	if record.Text != nil {
		message.Text = *record.Text
	}

	if record.EditedTS != "" {
		message.Edited = &slack.Edit{TS: record.EditedTS}
	}

	message.ReplyCount = record.ReplyCount

	for _, file := range record.Files {
		message.Files = append(message.Files, slack.File{
			ID:       file.ID,
			Name:     file.Name,
			Title:    file.Title,
			Mimetype: file.Mimetype,
			Size:     file.Size,
		})
	}

	return message
}
