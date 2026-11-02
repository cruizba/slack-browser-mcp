package slack_test

import (
	"strings"
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/slack"
)

func TestParsePrintedReference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		id   string
		rest string
	}{
		{name: "plain name", in: "general", rest: "general"},
		{name: "printed label", in: "#general (C01RANDOM)", id: "C01RANDOM", rest: "#general"},
		{name: "dm with a member count", in: "dm:Ana Ruiz (D01ANA) 2 members", id: "D01ANA", rest: "dm:Ana Ruiz"},
		{name: "one member", in: "dm:Ana Ruiz (D01ANA) 1 member", id: "D01ANA", rest: "dm:Ana Ruiz"},
		{name: "id alone", in: "C01RANDOM", rest: "C01RANDOM"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := slack.ParsePrintedReference(tt.in)
			if got.ID != tt.id || got.Reference != tt.rest {
				t.Fatalf("ParsePrintedReference(%q) = %+v, want id %q rest %q", tt.in, got, tt.id, tt.rest)
			}
		})
	}
}

func TestResolveReferences(t *testing.T) {
	t.Parallel()

	scope := &slack.ReferenceScope{
		User:      func(id string) string { return map[string]string{"U01ANA": "ana"}[id] },
		Channel:   func(id string) string { return map[string]string{"C01RANDOM": "random"}[id] },
		Usergroup: func(id string) string { return map[string]string{"S01TEAM": "core"}[id] },
	}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "mention", in: "hello <@U01ANA>", want: "hello @ana"},
		{name: "mention with a label wins", in: "<@U01ANA|Ana>", want: "@Ana"},
		{name: "unknown user keeps the id", in: "<@U99NOBODY>", want: "@U99NOBODY"},
		{name: "channel", in: "see <#C01RANDOM>", want: "see #random"},
		{name: "channel with a label", in: "<#C01RANDOM|random>", want: "#random"},
		{name: "subteam", in: "<!subteam^S01TEAM>", want: "@core"},
		{name: "subteam with a label", in: "<!subteam^S01TEAM|@core>", want: "@core"},
		{name: "special mention", in: "<!channel> heads up", want: "@channel heads up"},
		{name: "date keeps its fallback", in: "<!date^1518032400^{date_short}|Feb 7, 2018>", want: "Feb 7, 2018"},
		{name: "link with a label", in: "<https://example.com|there>", want: "there (https://example.com)"},
		{name: "bare link", in: "<https://example.com>", want: "https://example.com"},
		{name: "entities are undone", in: "a &lt;b&gt; &amp; c", want: "a <b> & c"},
		{name: "plain text is untouched", in: "no markup here", want: "no markup here"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := slack.ResolveReferences(tt.in, scope); got != tt.want {
				t.Fatalf("ResolveReferences(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestEntitiesRoundTrip(t *testing.T) {
	t.Parallel()

	// The decoder runs &amp; last, so text that wanted a literal &lt; survives.
	if got := slack.UnescapeEntities("&amp;lt;"); got != "&lt;" {
		t.Fatalf("UnescapeEntities(&amp;lt;) = %q", got)
	}

	if got := slack.EscapeEntities("a & b < c > d"); got != "a &amp; b &lt; c &gt; d" {
		t.Fatalf("EscapeEntities = %q", got)
	}
}

func TestFormatTS(t *testing.T) {
	t.Parallel()

	if got := slack.FormatTS("1792500000.000100"); got != "2026-10-20 12:40:00" {
		t.Fatalf("FormatTS = %q", got)
	}

	if got := slack.FormatTS("not-a-ts"); got != "not-a-ts" {
		t.Fatalf("FormatTS kept nothing of a broken ts: %q", got)
	}
}

func TestConversationLabel(t *testing.T) {
	t.Parallel()

	directory := &slack.Directory{
		Users: []slack.DirectoryUser{
			{ID: "U01ANA", Name: "ana", DisplayName: "Ana Ruiz"},
			{ID: "U02MICHEL", Name: "michel"},
		},
		Channels: []slack.DirectoryChannel{
			{ID: "C01RANDOM", Kind: slack.KindChannel, Name: "random"},
			{ID: "C02LEADS", Kind: slack.KindPrivate, Name: "leads"},
			{ID: "D01ANA", Kind: slack.KindDM, User: "U01ANA"},
			{ID: "G01GROUP", Kind: slack.KindMpIM, Name: "mpdm-ana--michel-1"},
		},
	}

	tests := []struct {
		name string
		ref  slack.ConversationRef
		want string
	}{
		{name: "channel", ref: slack.ConversationRef{ID: "C01RANDOM"}, want: "#random"},
		{name: "private", ref: slack.ConversationRef{ID: "C02LEADS"}, want: "private:leads"},
		{name: "dm names the person", ref: slack.ConversationRef{ID: "D01ANA"}, want: "dm:Ana Ruiz"},
		{name: "group dm lists the people", ref: slack.ConversationRef{ID: "G01GROUP"}, want: "group:Ana Ruiz, michel"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := slack.ConversationLabel(tt.ref, directory); got != tt.want {
				t.Fatalf("ConversationLabel(%+v) = %q, want %q", tt.ref, got, tt.want)
			}
		})
	}
}

func TestRenderMessages(t *testing.T) {
	t.Parallel()

	messages := []slack.Message{
		{TS: "1792500000.000100", User: "U01ANA", Text: "hello <@U02MICHEL>", ReplyCount: 2},
		{TS: "1792500060.000200", User: "U01ANA", Text: "line one\nline two", ThreadTS: "1792500000.000100"},
		{TS: "1792500120.000300", Username: "bot", Text: ""},
	}

	rendered := slack.RenderMessages(messages, slack.RenderOptions{
		Names: map[string]string{"U01ANA": "Ana"},
		Scope: &slack.ReferenceScope{User: func(string) string { return "Michel" }},
	})

	want := "[1792500000.000100] 2026-10-20 12:40:00 Ana: hello @Michel  (2 replies, thread_ts 1792500000.000100)\n" +
		"[1792500060.000200] 2026-10-20 12:41:00 Ana: line one\n    line two\n" +
		"[1792500120.000300] 2026-10-20 12:42:00 bot: "

	if rendered != want {
		t.Fatalf("RenderMessages:\n%s\nwant:\n%s", rendered, want)
	}
}

func TestMessageTruncation(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", slack.MaxMessageChars+10)
	rendered := slack.RenderMessages([]slack.Message{{TS: "1792500000.000100", User: "U01ANA", Text: long}}, slack.RenderOptions{})

	if !strings.Contains(rendered, "truncated: 4010 characters") {
		t.Fatalf("a long message was not marked as truncated: %s", rendered[len(rendered)-120:])
	}
}

func TestFileLabelKeepsTheID(t *testing.T) {
	t.Parallel()

	rendered := slack.RenderMessages([]slack.Message{{
		TS:    "1792500000.000100",
		User:  "U01ANA",
		Files: []slack.File{{ID: "F01FILE", Name: "report.pdf"}},
	}}, slack.RenderOptions{})

	if !strings.Contains(rendered, "[file: report.pdf id=F01FILE]") {
		t.Fatalf("the file id did not travel with the name: %q", rendered)
	}
}
