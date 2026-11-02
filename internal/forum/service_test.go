package forum_test

import (
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/archive"
	"github.com/cruizba/slack-browser-mcp/internal/forum"
)

// serviceFixture is a forum with one bound channel, one registered identity and an
// archive of its own, so the service sees what a real machine would.
func serviceFixture(t *testing.T) (forum.Context, *archive.Store) {
	t.Helper()

	root := t.TempDir()
	store := forum.NewStore(root + "/forum.json")

	if _, err := store.Bind(forum.Binding{
		Workspace: "T01ACME", Channel: "C01FORUM", Name: "forum",
		SinceTS: "0", AddedAt: "2026-11-02T10:00:00.000Z", Announce: true,
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	if _, err := store.Register("me", forum.StoredIdentity{
		Name: "ana", Host: "laptop", RegisteredAt: "2026-11-02T10:00:00.000Z",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	archiveStore := archive.NewStore(root + "/archive")

	context, err := forum.OpenForum(forum.OpenOptions{
		ClientName: "me",
		Now:        time.Date(2026, 11, 2, 10, 10, 0, 0, time.UTC),
		Store:      store,
		Archive:    archiveStore,
		Decisions:  forum.NewDecisionLog(root + "/decisions"),
		AuditLog:   root + "/audit.jsonl",
	})
	if err != nil {
		t.Fatalf("OpenForum: %v", err)
	}

	return context, archiveStore
}

// nextTS hands out distinct Slack stamps: two messages with the same ts are one
// message as far as the archive is concerned. It is atomic because the tests that use
// it run in parallel.
var nextTS atomic.Int64

func init() {
	nextTS.Store(1793200000)
}

func stamp() string {
	return strconv.FormatInt(nextTS.Add(1), 10) + ".000100"
}

// seedPeer writes a register envelope into the archive, which is the only way a name
// enters a roster.
func seedPeer(t *testing.T, store *archive.Store, name, host string) {
	t.Helper()

	envelope := &forum.Envelope{
		V: 1, Kind: forum.KindRegister, ID: forum.NewID(time.Now()),
		From: forum.LabelOf(name, host), Name: name, Host: host,
		Sent: "2026-11-02T09:00:00.000Z",
	}

	appendEnvelope(t, store, envelope, stamp())
}

// seedTask writes a task envelope, which is what opens a ledger entry.
func seedTask(t *testing.T, store *archive.Store, from, to, title string) string {
	t.Helper()

	depth := 0

	envelope := &forum.Envelope{
		V: 1, Kind: forum.KindTask, ID: forum.NewID(time.Now()),
		From: from, To: to, Title: title, Body: "do the thing", HasBody: true,
		Sent: "2026-11-02T09:30:00.000Z", Depth: &depth,
	}

	appendEnvelope(t, store, envelope, stamp())

	return envelope.ID
}

func appendEnvelope(t *testing.T, store *archive.Store, envelope *forum.Envelope, ts string) {
	t.Helper()

	document, err := envelope.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	if err := store.Append("T01ACME", "C01FORUM", []archive.ArchivedMessage{{
		TS:       ts,
		Chan:     "C01FORUM",
		User:     "U01ANA",
		Forum:    document,
		SyncedAt: "2026-11-02T09:00:00.000Z",
	}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
}

func TestLocateTaskRefusesAmbiguity(t *testing.T) {
	t.Parallel()

	context, store := serviceFixture(t)
	seedPeer(t, store, "builder", "laptop")

	first := seedTask(t, store, "ana@laptop", "builder@laptop", "one")
	second := seedTask(t, store, "ana@laptop", "builder@laptop", "two")

	views := forum.LoadViews(context, forum.ViewFilter{})

	located, err := forum.LocateTask(views, first)
	if err != nil {
		t.Fatalf("LocateTask: %v", err)
	}

	if located.Task.Title != "one" {
		t.Fatalf("located %q", located.Task.Title)
	}

	// The first four characters are the same millisecond, so both tasks match.
	if _, err := forum.LocateTask(views, first[:6]); err == nil ||
		!strings.Contains(err.Error(), "matches") {
		t.Fatalf("an ambiguous prefix was resolved: %v", err)
	}

	if _, err := forum.LocateTask(views, "ZZZZ"); err == nil {
		t.Fatal("an unknown prefix was resolved")
	}

	_ = second
}

func TestPreviewShowsTheEnvelopeWithoutPosting(t *testing.T) {
	t.Parallel()

	context, store := serviceFixture(t)

	seedPeer(t, store, "builder", "laptop")

	result, err := forum.PostTask(context, nil, forum.PostTaskArgs{
		To: "builder@laptop", Title: "Sync it", Body: "the diff", By: "cli",
	}, nil)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}

	if result.Posted {
		t.Fatal("an unconfirmed post was posted")
	}

	if !strings.Contains(result.Text, "ana@laptop asks builder@laptop: Sync it") {
		t.Fatalf("the preview does not lead with the summary:\n%s", result.Text)
	}

	if !strings.Contains(result.Text, "```markdown") {
		t.Fatalf("the preview does not show the envelope:\n%s", result.Text)
	}

	// A preview changes nothing: no task appears in the ledger.
	for _, view := range forum.LoadViews(context, forum.ViewFilter{}) {
		if len(view.Ledger.Tasks) != 0 {
			t.Fatalf("a preview wrote %d task(s) into the ledger", len(view.Ledger.Tasks))
		}
	}
}

func TestPostTaskRefusesABroadcastWithoutOneChannel(t *testing.T) {
	t.Parallel()

	context, _ := serviceFixture(t)

	if _, err := context.Store.Bind(forum.Binding{
		Workspace: "T01ACME", Channel: "C02FORUM", Name: "other",
		SinceTS: "0", AddedAt: "2026-11-02T10:00:00.000Z", Announce: true,
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	context, err := context.Reload()
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}

	_, err = forum.PostTask(context, nil, forum.PostTaskArgs{
		To: "*", Title: "anyone", Body: "body", By: "cli",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "A broadcast needs one channel") {
		t.Fatalf("err = %v", err)
	}
}

func TestDeadlineMustBeInTheFuture(t *testing.T) {
	t.Parallel()

	context, store := serviceFixture(t)
	seedPeer(t, store, "builder", "laptop")

	_, err := forum.PostTask(context, nil, forum.PostTaskArgs{
		To: "builder@laptop", Title: "late", Body: "body",
		Deadline: "2020-01-01T00:00:00Z", By: "cli",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "already past") {
		t.Fatalf("err = %v", err)
	}

	_, err = forum.PostTask(context, nil, forum.PostTaskArgs{
		To: "builder@laptop", Title: "late", Body: "body", Deadline: "tomorrow", By: "cli",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "is not a date") {
		t.Fatalf("err = %v", err)
	}
}

func TestReplyValidation(t *testing.T) {
	t.Parallel()

	context, store := serviceFixture(t)
	seedPeer(t, store, "builder", "laptop")

	taskID := seedTask(t, store, "builder@laptop", "ana@laptop", "Sync it")

	// open, addressed to me: a result needs an accepted task first.
	_, err := forum.ReplyTo(context, nil, forum.ReplyArgs{
		ID: taskID, Kind: forum.KindResult, Body: "done", By: "cli",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "not accepted") {
		t.Fatalf("a result on an open task was accepted: %v", err)
	}

	// A note needs a body, before anything else is looked at.
	_, err = forum.ReplyTo(context, nil, forum.ReplyArgs{
		ID: taskID, Kind: forum.KindProgress, Body: "", By: "cli",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "needs a body") {
		t.Fatalf("a progress note with no body was accepted: %v", err)
	}

	// The requester cannot claim their own task.
	own := seedTask(t, store, "ana@laptop", "builder@laptop", "Mine")

	_, err = forum.ReplyTo(context, nil, forum.ReplyArgs{
		ID: own, Kind: forum.KindAccept, By: "cli",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "addressed to") {
		t.Fatalf("the requester claimed their own task: %v", err)
	}
}

func TestPickBindingRoutesABareName(t *testing.T) {
	t.Parallel()

	context, store := serviceFixture(t)
	seedPeer(t, store, "builder", "laptop")

	result, err := forum.PostTask(context, nil, forum.PostTaskArgs{
		To: "builder", Title: "routed", Body: "body", By: "cli",
	}, nil)
	if err != nil {
		t.Fatalf("PostTask: %v", err)
	}

	// A bare name is resolved to name@host before the envelope is built, because that
	// is what the state machine compares against.
	if result.Envelope.To != "builder@laptop" {
		t.Fatalf("to = %q, want the exact agent", result.Envelope.To)
	}
}

func TestAnnounceOnlyWhenTheCapabilitiesChanged(t *testing.T) {
	t.Parallel()

	context, store := serviceFixture(t)
	seedPeer(t, store, "builder", "laptop")

	identity := forum.Identity{Name: "ana", Host: "laptop", Capabilities: []string{"review"}}

	// The channel has never heard of ana with a capability, so it has to be told.
	announce, err := forum.RegisterIdentity(context, identity)
	if err != nil {
		t.Fatalf("RegisterIdentity: %v", err)
	}

	if !announce {
		t.Fatal("a first register did not ask to announce")
	}
}
