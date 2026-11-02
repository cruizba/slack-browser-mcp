package slack_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/auth"
	"github.com/cruizba/slack-browser-mcp/internal/failure"
	"github.com/cruizba/slack-browser-mcp/internal/slack"
)

// fakeSlack is a Slack that answers whatever the test says, one method at a time, and
// remembers what it was asked.
type fakeSlack struct {
	t      *testing.T
	server *httptest.Server
	mutex  sync.Mutex
	// answers is the body per method, in order: a method with more answers than calls
	// keeps using the last one.
	answers map[string][]answer
	calls   []call
}

type answer struct {
	status int
	body   string
	header map[string]string
}

type call struct {
	method string
	form   url.Values
	header http.Header
}

func newFake(t *testing.T) *fakeSlack {
	t.Helper()

	fake := &fakeSlack{t: t, answers: map[string][]answer{}, calls: nil}

	fake.server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		form, _ := url.ParseQuery(string(body))
		method := strings.TrimPrefix(request.URL.Path, "/")

		fake.mutex.Lock()
		fake.calls = append(fake.calls, call{method: method, form: form, header: request.Header.Clone()})

		queued := fake.answers[method]

		var chosen answer

		switch {
		case len(queued) == 0:
			chosen = answer{status: http.StatusOK, body: `{"ok":true}`}
		case len(queued) == 1:
			chosen = queued[0]
		default:
			chosen = queued[0]
			fake.answers[method] = queued[1:]
		}
		fake.mutex.Unlock()

		status := chosen.status
		if status == 0 {
			status = http.StatusOK
		}

		for name, value := range chosen.header {
			response.Header().Set(name, value)
		}

		response.WriteHeader(status)
		_, _ = response.Write([]byte(chosen.body))
	}))

	t.Cleanup(fake.server.Close)

	return fake
}

func (f *fakeSlack) reply(method, body string) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	f.answers[method] = append(f.answers[method], answer{body: body})
}

func (f *fakeSlack) replyStatus(method string, status int, body string, header map[string]string) {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	f.answers[method] = append(f.answers[method], answer{status: status, body: body, header: header})
}

func (f *fakeSlack) callsTo(method string) []call {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	found := make([]call, 0, len(f.calls))

	for _, entry := range f.calls {
		if entry.method == method {
			found = append(found, entry)
		}
	}

	return found
}

func (f *fakeSlack) client(t *testing.T, pace time.Duration) *slack.Client {
	t.Helper()

	//nolint:gosec // placeholders: the fake only checks that both halves are present
	return slack.NewClient(auth.Account{
		TeamID:      "T01ACME",
		TeamName:    "Acme",
		UserID:      "U01ANA",
		Token:       "xoxc-test-token",
		DCookie:     "xoxd-test-cookie",
		DCookieName: "d",
	}, slack.Options{
		BaseURL:    f.server.URL + "/",
		Sleep:      func(time.Duration) {},
		Pace:       pace,
		MaxRetries: 2,
	})
}

func TestCallSendsTheSessionTheBrowserCaptured(t *testing.T) {
	t.Parallel()

	fake := newFake(t)
	fake.reply("auth.test", `{"ok":true,"team":"Acme","team_id":"T01ACME","user":"ana","user_id":"U01ANA"}`)

	info, err := fake.client(t, 0).AuthTest()
	if err != nil {
		t.Fatalf("AuthTest: %v", err)
	}

	if info.Team != "Acme" || info.UserID != "U01ANA" {
		t.Fatalf("AuthTest = %+v", info)
	}

	sent := fake.callsTo("auth.test")
	if len(sent) != 1 {
		t.Fatalf("calls = %d", len(sent))
	}

	if sent[0].form.Get("token") != "xoxc-test-token" {
		t.Fatalf("the token was not sent: %v", sent[0].form)
	}

	// The cookie is the other half of the session: Slack refuses the token alone.
	if cookie := sent[0].header.Get("Cookie"); cookie != "d=xoxd-test-cookie" {
		t.Fatalf("cookie = %q", cookie)
	}
}

func TestCallRetriesARateLimit(t *testing.T) {
	t.Parallel()

	fake := newFake(t)
	fake.replyStatus("conversations.history", http.StatusTooManyRequests, `{}`, map[string]string{"retry-after": "0"})
	fake.reply("conversations.history", `{"ok":true,"messages":[{"ts":"1793200000.000100"}]}`)

	page, err := fake.client(t, 0).History(slack.HistoryParams{Channel: "C01ACME", Limit: 10})
	if err != nil {
		t.Fatalf("History: %v", err)
	}

	if len(page.Messages) != 1 {
		t.Fatalf("messages = %d", len(page.Messages))
	}

	if calls := len(fake.callsTo("conversations.history")); calls != 2 {
		t.Fatalf("a rate limit was not retried: %d call(s)", calls)
	}
}

func TestCallGivesUpAfterTheRetryBudget(t *testing.T) {
	t.Parallel()

	fake := newFake(t)

	for range 5 {
		fake.replyStatus("conversations.history", http.StatusServiceUnavailable, `{}`, nil)
	}

	_, err := fake.client(t, 0).History(slack.HistoryParams{Channel: "C01ACME"})
	if err == nil {
		t.Fatal("a permanently unavailable Slack was accepted")
	}

	var apiError *failure.APIError

	if !errors.As(err, &apiError) || apiError.Code != "http_503" {
		t.Fatalf("err = %v", err)
	}

	// maxRetries is 2, so three attempts in total: the first, then two retries.
	if calls := len(fake.callsTo("conversations.history")); calls != 3 {
		t.Fatalf("attempts = %d, want 3", calls)
	}
}

func TestCallTurnsARejectedSessionIntoAnAuthError(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"invalid_auth", "token_revoked", "account_inactive"} {
		fake := newFake(t)
		fake.reply("auth.test", `{"ok":false,"error":"`+code+`"}`)

		_, err := fake.client(t, 0).AuthTest()
		if err == nil {
			t.Fatalf("%s was accepted", code)
		}

		var authError *failure.AuthError

		if !errors.As(err, &authError) {
			t.Fatalf("%s gave %T, want an AuthError", code, err)
		}

		// The hint is what the CLI prints next to the failure, so it has to name the fix.
		if !strings.Contains(authError.Hint, "slack-browser login") {
			t.Fatalf("hint = %q", authError.Hint)
		}
	}
}

func TestCallRetriesARetryableSlackError(t *testing.T) {
	t.Parallel()

	fake := newFake(t)
	fake.reply("users.list", `{"ok":false,"error":"ratelimited"}`)
	fake.reply("users.list", `{"ok":true,"members":[]}`)

	if _, err := fake.client(t, 0).ListUsers(); err != nil {
		t.Fatalf("ListUsers: %v", err)
	}

	if calls := len(fake.callsTo("users.list")); calls != 2 {
		t.Fatalf("calls = %d, want the ratelimited one retried", calls)
	}
}

func TestCallRefusesABodyThatIsNotJSON(t *testing.T) {
	t.Parallel()

	fake := newFake(t)
	fake.reply("auth.test", "<html>not an api</html>")

	_, err := fake.client(t, 0).AuthTest()
	if err == nil {
		t.Fatal("a non-JSON body was accepted")
	}

	var apiError *failure.APIError

	if !errors.As(err, &apiError) || !strings.HasPrefix(apiError.Code, "invalid_json_") {
		t.Fatalf("err = %v", err)
	}
}

// resolveFixture is the directory a channel is resolved against: an exact name, a
// longer one that contains it, and my own DM.
func resolveFixture(t *testing.T) *slack.Client {
	t.Helper()

	fake := newFake(t)
	fake.reply("users.conversations", `{"ok":true,"channels":[
		{"id":"C01GENERAL","name":"general"},
		{"id":"C02GENERAL2","name":"general-ops"},
		{"id":"D01ANA","is_im":true,"user":"U01ANA"}
	]}`)
	fake.reply("conversations.open", `{"ok":true,"channel":{"id":"D01SELF"}}`)

	return fake.client(t, 0)
}

func TestResolveChannelTakesAnIDAsItIs(t *testing.T) {
	t.Parallel()

	channel, err := resolveFixture(t).ResolveChannel("C09DIRECT", nil)
	if err != nil || channel.ID != "C09DIRECT" {
		t.Fatalf("ResolveChannel = %+v, %v", channel, err)
	}
}

func TestResolveChannelReadsAPrintedLabel(t *testing.T) {
	t.Parallel()

	channel, err := resolveFixture(t).ResolveChannel("#general (C01GENERAL)", nil)
	if err != nil || channel.ID != "C01GENERAL" {
		t.Fatalf("ResolveChannel = %+v, %v", channel, err)
	}
}

func TestResolveChannelPrefersAnExactName(t *testing.T) {
	t.Parallel()

	// `general` is a prefix of `general-ops`, and the exact one wins.
	channel, err := resolveFixture(t).ResolveChannel("#general", nil)
	if err != nil || channel.ID != "C01GENERAL" {
		t.Fatalf("ResolveChannel = %+v, %v", channel, err)
	}
}

func TestResolveChannelFindsAPerson(t *testing.T) {
	t.Parallel()

	channel, err := resolveFixture(t).ResolveChannel("@ana", func(name string) (string, bool) {
		return "U01ANA", name == "ana"
	})
	if err != nil || channel.ID != "D01ANA" {
		t.Fatalf("ResolveChannel(@ana) = %+v, %v", channel, err)
	}
}

func TestResolveChannelOpensMyOwnDM(t *testing.T) {
	t.Parallel()

	channel, err := resolveFixture(t).ResolveChannel("me", nil)
	if err != nil || channel.ID != "D01SELF" || channel.Name != "self-dm" {
		t.Fatalf("ResolveChannel(me) = %+v, %v", channel, err)
	}
}

func TestResolveChannelNamesWhatToRunWhenItFindsNothing(t *testing.T) {
	t.Parallel()

	_, err := resolveFixture(t).ResolveChannel("nowhere", nil)
	if err == nil || !strings.Contains(err.Error(), "list_conversations") {
		t.Fatalf("err = %v", err)
	}
}

func TestHistoryPaging(t *testing.T) {
	t.Parallel()

	fake := newFake(t)
	fake.reply("conversations.history", `{"ok":true,"messages":[{"ts":"1793200000.000100"}],"has_more":true,"response_metadata":{"next_cursor":"page2"}}`)

	page, err := fake.client(t, 0).History(slack.HistoryParams{Channel: "C01ACME", Limit: 5, Oldest: "1793100000.000000"})
	if err != nil {
		t.Fatalf("History: %v", err)
	}

	if !page.HasMore || page.NextCursor != "page2" {
		t.Fatalf("page = %+v", page)
	}

	sent := fake.callsTo("conversations.history")[0].form

	if sent.Get("oldest") != "1793100000.000000" || sent.Get("limit") != "5" {
		t.Fatalf("the window was not sent: %v", sent)
	}
}

func TestNamesForStopsAtTheLookupCap(t *testing.T) {
	t.Parallel()

	fake := newFake(t)

	messages := make([]slack.Message, 0, 40)

	for index := range 40 {
		messages = append(messages, slack.Message{TS: "1793200000.00010" + string(rune('0'+index%10)), User: fmt.Sprintf("U%04d", index)})
	}

	for index := range 40 {
		fake.reply("users.info", fmt.Sprintf(`{"ok":true,"user":{"name":"person%d"}}`, index))
	}

	names := fake.client(t, 0).NamesFor(messages, nil)

	// Without a directory every author costs a request, so past the cap the id is shown
	// rather than turning one read into forty.
	if len(names) > 25 {
		t.Fatalf("names = %d, want at most 25 lookups", len(names))
	}

	if calls := len(fake.callsTo("users.info")); calls > 25 {
		t.Fatalf("users.info called %d times", calls)
	}
}

func TestNamesForPrefersTheDirectory(t *testing.T) {
	t.Parallel()

	fake := newFake(t)

	messages := []slack.Message{{TS: "1793200000.000100", User: "U01ANA"}}

	names := fake.client(t, 0).NamesFor(messages, func(id string) (string, bool) {
		return "Ana", id == "U01ANA"
	})

	if names["U01ANA"] != "Ana" {
		t.Fatalf("names = %v", names)
	}

	if calls := len(fake.callsTo("users.info")); calls != 0 {
		t.Fatalf("the directory was not used: %d users.info call(s)", calls)
	}
}

func TestUploadReportsTheMessageSlackMade(t *testing.T) {
	t.Parallel()

	fake := newFake(t)
	fake.reply("files.getUploadURLExternal", `{"ok":true,"upload_url":"`+fake.server.URL+`/upload/F01","file_id":"F01"}`)
	fake.reply("files.completeUploadExternal", `{"ok":true,"files":[{"id":"F01","shares":{"public":{"C01ACME":[{"ts":"1793200000.000100"}]}}}]}`)

	posted, err := fake.client(t, 0).UploadFiles("C01ACME", []slack.Upload{{Name: "report.md", Bytes: []byte("# hi")}}, "here it is", "")
	if err != nil {
		t.Fatalf("UploadFiles: %v", err)
	}

	if posted.TS != "1793200000.000100" {
		t.Fatalf("ts = %q, want the message Slack made", posted.TS)
	}

	if len(posted.FileIDs) != 1 || posted.FileIDs[0] != "F01" {
		t.Fatalf("fileIds = %v", posted.FileIDs)
	}

	complete := fake.callsTo("files.completeUploadExternal")[0].form

	if complete.Get("initial_comment") != "here it is" {
		t.Fatalf("the comment did not travel with the upload: %v", complete)
	}

	var described []map[string]string
	if err := json.Unmarshal([]byte(complete.Get("files")), &described); err != nil {
		t.Fatalf("files = %v", err)
	}

	if described[0]["title"] != "report.md" {
		t.Fatalf("described = %v", described)
	}
}

func TestPostMessageSendsBlocks(t *testing.T) {
	t.Parallel()

	fake := newFake(t)
	fake.reply("chat.postMessage", `{"ok":true,"ts":"1793200000.000100","channel":"C01ACME","message":{"text":"hi"}}`)

	posted, err := fake.client(t, 0).PostMessage("C01ACME", "hi", "1793100000.000000", []any{map[string]any{"type": "divider"}})
	if err != nil {
		t.Fatalf("PostMessage: %v", err)
	}

	if posted.TS != "1793200000.000100" || posted.Text != "hi" {
		t.Fatalf("posted = %+v", posted)
	}

	sent := fake.callsTo("chat.postMessage")[0].form

	if sent.Get("thread_ts") != "1793100000.000000" {
		t.Fatalf("thread_ts = %q", sent.Get("thread_ts"))
	}

	if !strings.Contains(sent.Get("blocks"), "divider") {
		t.Fatalf("blocks = %q", sent.Get("blocks"))
	}
}
