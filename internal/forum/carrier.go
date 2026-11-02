package forum

import (
	"strings"

	"github.com/cruizba/slack-browser-mcp/internal/archive"
	"github.com/cruizba/slack-browser-mcp/internal/envelope"
)

// Carrier is the part of a message an envelope can be read from: the text, the file
// contents the sync kept inline, and the attachments the file names live in.
type Carrier struct {
	Text     string
	Forum    string
	HasForum bool
	Files    []archive.ArchivedFile
}

// CarrierOf is the carrier of an archived message.
func CarrierOf(message archive.ArchivedMessage) Carrier {
	return Carrier{
		Text:     message.Body(),
		Forum:    message.Forum,
		HasForum: message.Forum != "",
		Files:    message.Files,
	}
}

// EnvelopeOf answers the envelope a message carries: the file first, the legacy
// fence second.
func EnvelopeOf(carrier Carrier) *Envelope {
	if carrier.HasForum {
		return decodeCarried(carrier.Forum, envelopeName(carrier.Files))
	}

	return DecodeFence(carrier.Text)
}

func decodeCarried(text, name string) *Envelope {
	switch {
	case strings.HasSuffix(name, envelope.FileSuffix):
		return DecodeMarkdown(text)
	case strings.HasSuffix(name, envelope.LegacySuffix):
		return DecodeJSON(text, false)
	}

	// A record synced before the file entry was kept has the text and no name, so the
	// first line decides: a JSON envelope always opens with a brace.
	if strings.HasPrefix(strings.TrimLeft(text, " \t\r\n"), "{") {
		return DecodeJSON(text, false)
	}

	return DecodeMarkdown(text)
}

func envelopeName(files []archive.ArchivedFile) string {
	carried := make([]envelope.CarriedFile, 0, len(files))

	for _, file := range files {
		carried = append(carried, envelope.CarriedFile{Name: file.Name, Size: file.Size})
	}

	chosen, ok := envelope.FileOf(carried)
	if !ok {
		return ""
	}

	return chosen.Name
}

// CarriesEnvelope is true when the message claims to carry an envelope, readable or
// not. A record that names a `forum-*` but has no forum text yet is a message that
// arrived before the sync could fetch it: counting it as prose would hide it behind a
// perfectly ordinary sentence, which is how a whole thread went missing.
func CarriesEnvelope(carrier Carrier) bool {
	if carrier.HasForum {
		return true
	}

	if _, carried := envelopeNameOf(carrier.Files); carried {
		return true
	}

	return strings.Contains(carrier.Text, "```forum")
}

func envelopeNameOf(files []archive.ArchivedFile) (string, bool) {
	name := envelopeName(files)

	return name, name != ""
}
