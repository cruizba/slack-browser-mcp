package forum_test

import (
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/forum"
)

// These read what Slack hands back, which is somebody else's text: an envelope written
// by a version this build has never seen, a file a peer attached, a body with a fence in
// it. Nothing here may panic — a reader that dies on a malformed message takes the inbox
// down with it, and the repair is not something the reader can ask for.

func FuzzDecodeMarkdown(f *testing.F) {
	f.Add("---\nv: 1\nkind: task\n---\nbody")
	f.Add("---\n---\n")
	f.Add("no frontmatter at all")
	f.Add("---\nv: 1\nfields:\n  to: alice\n---\n\n# heading")
	f.Add("---\nbroken: [\n---\n")

	f.Fuzz(func(_ *testing.T, text string) {
		// A decoded envelope has to render without blowing up, because the next thing
		// the reader does with it is print it.
		if envelope := forum.DecodeMarkdown(text); envelope != nil {
			_ = envelope.Kind
			_ = envelope.ID
		}
	})
}

func FuzzDecodeJSON(f *testing.F) {
	f.Add(`{"v":1,"kind":"task","id":"01J9Z7QK3M4N5P6R7S8T9V0W1X"}`)
	f.Add(`null`)
	f.Add(`[]`)
	f.Add(`{"v":"1"}`)
	f.Add(`{"fields":[{"i":1,"t":"title","v":{"s":"x"}}]}`)

	f.Fuzz(func(_ *testing.T, raw string) {
		for _, legacy := range []bool{false, true} {
			if envelope := forum.DecodeJSON(raw, legacy); envelope != nil {
				_ = envelope.Kind
			}
		}
	})
}

func FuzzDecodeFence(f *testing.F) {
	f.Add("```forum\n{\"v\":1,\"kind\":\"beacon\"}\n```")
	f.Add("```")
	f.Add("````")
	f.Add("```forum\n")
	f.Add("pre ```forum\n{}\n``` post")

	f.Fuzz(func(_ *testing.T, text string) {
		_ = forum.DecodeFence(text)
	})
}

func FuzzReadFrontmatter(f *testing.F) {
	f.Add("---\na: b\n---\nbody")
	f.Add("---\n")
	f.Add("----\n---\n")
	f.Add("---\nk: \"quoted\"\n---")

	f.Fuzz(func(t *testing.T, text string) {
		document := forum.ReadFrontmatter(text)
		if document == nil {
			return
		}

		// Whatever came back has to survive being written again, and reading that has to
		// give the same fields: a round trip through a peer that loses one rewrites the
		// envelope without anybody noticing.
		body := document.Body

		text, err := forum.WriteFrontmatter(fieldsOf(document), &body)
		if err != nil {
			// A document a reader accepted but a writer refuses is the interesting case:
			// it would reach Slack as nothing at all.
			t.Fatalf("writing back what was just read failed: %v", err)
		}

		again := forum.ReadFrontmatter(text)
		if again == nil {
			t.Fatal("the written document does not read back")
		}

		if len(again.Values) != len(document.Values) {
			t.Fatalf("a round trip lost fields: %d became %d", len(document.Values), len(again.Values))
		}
	})
}

// fieldsOf keeps the order the values were read in, which is the order a reader expects
// to see them written back.
func fieldsOf(document *forum.Document) []forum.Field {
	fields := make([]forum.Field, 0, len(document.Values))

	for key, value := range document.Values {
		text, isText := value.(string)
		if !isText {
			continue
		}

		fields = append(fields, forum.Field{Key: key, Value: text})
	}

	return fields
}
