package forum_test

import (
	"strings"
	"testing"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/archive"
	"github.com/cruizba/slack-browser-mcp/internal/forum"
)

func task() *forum.Envelope {
	depth := 0

	return &forum.Envelope{
		V:       1,
		Kind:    forum.KindTask,
		ID:      "01J9Z7QK3M4N5P6R7S8T9V0W1X",
		From:    "builder@laptop",
		To:      "reviewer@laptop",
		Sent:    "2026-11-02T10:00:00.000Z",
		Title:   "Review the sync",
		Body:    "The diff is in the thread.\n\n![shot](x.png)",
		HasBody: true,
		Parent:  "01J9Z7QK3M4N5P6R7S8T9V0W2Y",
		Depth:   &depth,
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	t.Parallel()

	encoded, err := task().Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	if !strings.HasPrefix(encoded, "---\n") {
		t.Fatalf("the attachment does not open with a delimiter:\n%s", encoded)
	}

	fields, body, _ := strings.Cut(strings.TrimPrefix(encoded, "---\n"), "\n---\n")

	if !strings.Contains(fields, `title: "Review the sync"`) {
		t.Fatalf("fields:\n%s", fields)
	}

	if !strings.Contains(fields, `v: 1`) || !strings.Contains(fields, `kind: "task"`) {
		t.Fatalf("fields:\n%s", fields)
	}

	if !strings.HasSuffix(body, "![shot](x.png)") {
		t.Fatalf("the body did not survive verbatim:\n%s", body)
	}

	decoded := forum.DecodeMarkdown(encoded)
	if decoded == nil {
		t.Fatal("DecodeMarkdown refused what Encode wrote")
	}

	if decoded.ID != task().ID || decoded.Title != "Review the sync" || decoded.Body != task().Body {
		t.Fatalf("round trip lost something: %+v", decoded)
	}
}

func TestEnvelopeRefusesWhatTheSchemaRefuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*forum.Envelope)
	}{
		{name: "wrong version", mutate: func(e *forum.Envelope) { e.V = 2 }},
		{name: "short id", mutate: func(e *forum.Envelope) { e.ID = "01J9" }},
		{name: "no destination", mutate: func(e *forum.Envelope) { e.To = "" }},
		{name: "title too long", mutate: func(e *forum.Envelope) { e.Title = strings.Repeat("a", 121) }},
		{name: "no body", mutate: func(e *forum.Envelope) { e.HasBody = false }},
		{name: "tag on a unicast", mutate: func(e *forum.Envelope) { e.Tag = "review" }},
		{name: "depth out of range", mutate: func(e *forum.Envelope) { d := 10; e.Depth = &d }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			envelope := task()
			tt.mutate(envelope)

			if err := envelope.Validate(); err == nil {
				t.Fatal("Validate accepted an envelope the schema refuses")
			}
		})
	}
}

func TestAnnouncementsMustNameTheirHost(t *testing.T) {
	t.Parallel()

	register := &forum.Envelope{
		V: 1, Kind: forum.KindRegister, ID: "01J9Z7QK3M4N5P6R7S8T9V0W1X",
		From: "builder", Name: "builder", Host: "laptop", Sent: "2026-11-02T10:00:00.000Z",
	}

	if err := register.Validate(); err == nil {
		t.Fatal("a register whose from is not name@host was accepted")
	}

	register.From = "builder@laptop"

	if err := register.Validate(); err != nil {
		t.Fatalf("a well formed register was refused: %v", err)
	}
}

func TestDecodeIsForgiving(t *testing.T) {
	t.Parallel()

	for _, text := range []string{
		"just prose",
		"---\nkind: \"task\"\n---\n",
		"---\nnot a key: 1\n---\n",
		"---\nv: 1\nv: 2\n---\n",
		"",
	} {
		if envelope := forum.DecodeMarkdown(text); envelope != nil {
			t.Fatalf("DecodeMarkdown(%q) invented an envelope", text)
		}
	}
}

func TestLegacyFenceIsDecodedWithEscapingUndone(t *testing.T) {
	t.Parallel()

	// What Slack stored for a fenced envelope: the values arrived with entities.
	text := "a summary\n\n```forum\n" +
		`{"v":1,"kind":"register","id":"01J9Z7QK3M4N5P6R7S8T9V0W1X","from":"builder@laptop","name":"builder","host":"laptop","sent":"2026-11-02T10:00:00.000Z","capabilities":["review"],"version":"a &amp; b"}` +
		"\n```"

	envelope := forum.DecodeFence(text)
	if envelope == nil {
		t.Fatal("DecodeFence refused a legacy envelope")
	}

	if envelope.Version != "a & b" {
		t.Fatalf("the escaping was not undone: %q", envelope.Version)
	}
}

func TestFrontmatterDialect(t *testing.T) {
	t.Parallel()

	body := "line one\n---\nline two"

	encoded, err := forum.WriteFrontmatter([]forum.Field{
		{Key: "v", Value: 1},
		{Key: "ok", Value: true},
		{Key: "when", Value: "2026-11-02"},
		{Key: "tags", Value: []string{"a", "b"}},
	}, &body)
	if err != nil {
		t.Fatalf("WriteFrontmatter: %v", err)
	}

	document := forum.ReadFrontmatter(encoded)
	if document == nil {
		t.Fatal("ReadFrontmatter refused what WriteFrontmatter wrote")
	}

	if document.Values["v"] != float64(1) || document.Values["ok"] != true {
		t.Fatalf("values: %+v", document.Values)
	}

	if document.Body != body {
		t.Fatalf("the body with a delimiter inside did not survive: %q", document.Body)
	}
}

func TestSummaryFor(t *testing.T) {
	t.Parallel()

	if got := forum.SummaryFor(task()); got != "builder@laptop asks reviewer@laptop: Review the sync" {
		t.Fatalf("SummaryFor(task) = %q", got)
	}

	failed := task()
	failed.Kind = forum.KindResult
	failed.Task = failed.ID
	ok := false
	failed.OK = &ok
	failed.Body = "the tests fail\nmore detail"

	if got := forum.SummaryFor(failed); got != "builder@laptop could not finish "+failed.Task+": the tests fail" {
		t.Fatalf("SummaryFor(result) = %q", got)
	}
}

func TestRosterFromAnnouncements(t *testing.T) {
	t.Parallel()

	messages := []archive.ArchivedMessage{
		registerMessage(t, "01J9Z7QK3M4N5P6R7S8T9V0W1X", "builder", "laptop", "1793200000.000100"),
		beaconMessage(t, "01J9Z7QK3M4N5P6R7S8T9V0W2X", "reviewer", "desk", "1793200100.000100"),
		presenceMessage(t, "01J9Z7QK3M4N5P6R7S8T9V0W3X", "builder", "laptop", "away", "1793200200.000100"),
	}

	roster := forum.RosterFrom(messages, nil)

	if len(roster) != 2 {
		t.Fatalf("roster = %+v", roster)
	}

	builder, ok := forum.PeerFor(roster, "builder@laptop")
	if !ok {
		t.Fatal("builder is missing")
	}

	if builder.State != "away" || builder.Note != "" {
		t.Fatalf("the presence did not land: %+v", builder)
	}

	if len(builder.Capabilities) != 1 || builder.Capabilities[0] != "review" {
		t.Fatalf("the capabilities did not survive the later presence: %+v", builder)
	}

	reviewer, _ := forum.PeerFor(roster, "reviewer@desk")
	if reviewer.InteractiveTS != "" {
		t.Fatal("a beacon made the peer look busy")
	}
}

func TestRosterHonoursGoneAndRetired(t *testing.T) {
	t.Parallel()

	gone := presenceMessage(t, "01J9Z7QK3M4N5P6R7S8T9V0W3X", "builder", "laptop", "gone", "1793200200.000100")

	messages := []archive.ArchivedMessage{
		registerMessage(t, "01J9Z7QK3M4N5P6R7S8T9V0W1X", "builder", "laptop", "1793200000.000100"),
		gone,
	}

	if roster := forum.RosterFrom(messages, nil); len(roster) != 0 {
		t.Fatalf("a gone peer is still in the roster: %+v", roster)
	}

	messages = messages[:1]

	if roster := forum.RosterFrom(messages, []string{"builder@laptop"}); len(roster) != 0 {
		t.Fatalf("a retired peer is still in the roster: %+v", roster)
	}
}

func TestResolvePeer(t *testing.T) {
	t.Parallel()

	roster := []forum.Peer{
		{From: "builder@laptop", Name: "builder", Host: "laptop"},
		{From: "builder@desk", Name: "builder", Host: "desk"},
	}

	target, err := forum.ParseTarget("builder@desk")
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}

	peer, err := forum.ResolvePeer(roster, target)
	if err != nil || peer.From != "builder@desk" {
		t.Fatalf("ResolvePeer = %+v, %v", peer, err)
	}

	ambiguous, _ := forum.ParseTarget("builder")
	if _, err := forum.ResolvePeer(roster, ambiguous); err == nil {
		t.Fatal("an ambiguous name was resolved")
	}

	anyone, err := forum.ParseTarget("*")
	if err != nil || !anyone.Anyone {
		t.Fatalf("ParseTarget(*) = %+v, %v", anyone, err)
	}

	if _, err := forum.ParseTarget(""); err == nil {
		t.Fatal("an empty destination was accepted")
	}

	if _, err := forum.ParseTarget("a@b@c"); err == nil {
		t.Fatal("a destination with two @ was accepted")
	}
}

func TestIdsAreOrderedAndPrefixable(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

	first := forum.NewID(at)
	second := forum.NewID(at)

	if len(first) != forum.IDLength || !forum.IsID(first) {
		t.Fatalf("NewID = %q", first)
	}

	if second <= first {
		t.Fatalf("ids from the same millisecond are not ordered: %q then %q", first, second)
	}

	if !forum.MatchesID(first, strings.ToLower(first[:6])) {
		t.Fatal("a lowercase prefix did not match")
	}

	if forum.MatchesID(first, first[:3]) {
		t.Fatal("a prefix shorter than four characters matched")
	}

	if forum.NormalizeID("o1il") != "0111" {
		t.Fatalf("NormalizeID folded the wrong letters: %q", forum.NormalizeID("o1il"))
	}
}

func TestLimits(t *testing.T) {
	t.Parallel()

	if got := forum.LimitsWith(nil); got != forum.DefaultLimits {
		t.Fatalf("LimitsWith(nil) = %+v", got)
	}

	partial := forum.Limits{TTLHours: 48}

	if got := forum.LimitsWith(&partial); got.TTLHours != 48 || got.AckSeconds != 900 {
		t.Fatalf("a partial file did not keep the defaults: %+v", got)
	}

	if forum.RefreshWindowDays(forum.DefaultLimits) != 1 {
		t.Fatal("a one-day ttl should re-read one day")
	}

	want := "ttl 24h · ack 15m · stall 30m · here 1h · beacon 24h"
	if got := forum.DescribeLimits(forum.DefaultLimits); got != want {
		t.Fatalf("DescribeLimits = %q, want %q", got, want)
	}
}

func registerMessage(t *testing.T, id, name, host, ts string) archive.ArchivedMessage {
	t.Helper()

	envelope := &forum.Envelope{
		V: 1, Kind: forum.KindRegister, ID: id, From: name + "@" + host,
		Name: name, Host: host, Sent: "2026-11-02T10:00:00.000Z",
		Capabilities: []string{"review"}, Version: "0.8.0",
	}

	return archived(t, envelope, ts)
}

func beaconMessage(t *testing.T, id, name, host, ts string) archive.ArchivedMessage {
	t.Helper()

	envelope := &forum.Envelope{
		V: 1, Kind: forum.KindBeacon, ID: id, From: name + "@" + host,
		Name: name, Host: host, Sent: "2026-11-02T10:00:00.000Z",
	}

	return archived(t, envelope, ts)
}

func presenceMessage(t *testing.T, id, name, host, state, ts string) archive.ArchivedMessage {
	t.Helper()

	envelope := &forum.Envelope{
		V: 1, Kind: forum.KindPresence, ID: id, From: name + "@" + host,
		Name: name, Host: host, State: state, Sent: "2026-11-02T10:00:00.000Z",
	}

	return archived(t, envelope, ts)
}

func archived(t *testing.T, envelope *forum.Envelope, ts string) archive.ArchivedMessage {
	t.Helper()

	encoded, err := envelope.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	return archive.ArchivedMessage{
		TS:       ts,
		Chan:     "C01FORUM",
		User:     "U01ANA",
		Forum:    encoded,
		SyncedAt: "2026-11-02T10:00:00.000Z",
	}
}
