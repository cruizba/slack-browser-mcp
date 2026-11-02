package slack

import "strings"

// Slack stores `&`, `<` and `>` as HTML entities and hands `text` back that way,
// so a reader meets `&lt;` where the author typed `<`. Only those three are
// escaped in this direction: they are the ones Slack actually rewrites, and
// predicting more would make a sender refuse bodies that arrive intact.
var escaped = []struct{ from, to string }{
	{"&", "&amp;"},
	{"<", "&lt;"},
	{">", "&gt;"},
}

// `&amp;` is unescaped last: doing it first would turn `&amp;lt;` into `<` and
// lose the text. The decoder is wider than the encoder on purpose — it also
// undoes `&quot;` and `&#39;`, which other clients and older messages carry.
var unescaped = []struct{ from, to string }{
	{"&lt;", "<"},
	{"&gt;", ">"},
	{"&#39;", "'"},
	{"&quot;", `"`},
	{"&amp;", "&"},
}

// EscapeEntities is what Slack will show once it stores the text.
func EscapeEntities(text string) string {
	for _, entry := range escaped {
		text = strings.ReplaceAll(text, entry.from, entry.to)
	}

	return text
}

// UnescapeEntities is what a person would have seen.
func UnescapeEntities(text string) string {
	for _, entry := range unescaped {
		text = strings.ReplaceAll(text, entry.from, entry.to)
	}

	return text
}
