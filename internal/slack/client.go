// Package slack is a thin Slack Web API client. It authenticates with the xoxc
// token from the browser session plus its `d` cookie, which is the pair Slack's
// own web client sends; nothing here touches a DOM.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/auth"
	"github.com/cruizba/slack-browser-mcp/internal/failure"
	"github.com/cruizba/slack-browser-mcp/internal/redact"
)

const (
	methodUpload   = "files.upload"
	methodDownload = "files.download"
	codeBadRequest = "bad_request"
	codeNetwork    = "network_error"
	paramChannel   = "channel"
	paramLimit     = "limit"

	defaultAPIBase = "https://slack.com/api/"
	defaultPace    = 500 * time.Millisecond
	defaultBurst   = 4
	defaultRetries = 3
	maxNameLookups = 25
)

// APITimeout bounds one request, so a stalled connection cannot hang a sync.
const APITimeout = 60 * time.Second

var reauthErrors = map[string]bool{
	"invalid_auth":     true,
	"not_authed":       true,
	"token_revoked":    true,
	"token_expired":    true,
	"account_inactive": true,
}

var retryableErrors = map[string]bool{
	"ratelimited":         true,
	"internal_error":      true,
	"fatal_error":         true,
	"service_unavailable": true,
	"request_timeout":     true,
}

// AuthInfo is what `auth.test` says about the stored session.
type AuthInfo struct {
	Team   string
	TeamID string
	User   string
	UserID string
	URL    string
}

// Channel is a conversation, as Slack describes it.
type Channel struct {
	ID         string        `json:"id"`
	Name       string        `json:"name,omitempty"`
	IsPrivate  bool          `json:"is_private,omitempty"`
	IsIM       bool          `json:"is_im,omitempty"`
	IsMpIM     bool          `json:"is_mpim,omitempty"`
	IsArchived bool          `json:"is_archived,omitempty"`
	User       string        `json:"user,omitempty"`
	NumMembers int           `json:"num_members,omitempty"`
	Topic      *ChannelTopic `json:"topic,omitempty"`
}

// ChannelTopic is the one field of a channel topic a caller reads.
type ChannelTopic struct {
	Value string `json:"value,omitempty"`
}

// File is an attachment a message carries.
type File struct {
	ID                 string         `json:"id,omitempty"`
	Name               string         `json:"name,omitempty"`
	Title              string         `json:"title,omitempty"`
	Mimetype           string         `json:"mimetype,omitempty"`
	Size               int64          `json:"size,omitempty"`
	URLPrivate         string         `json:"url_private,omitempty"`
	URLPrivateDownload string         `json:"url_private_download,omitempty"`
	Shares             map[string]any `json:"shares,omitempty"`
}

// User is a workspace member.
type User struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	RealName    string `json:"real_name,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Deleted     bool   `json:"deleted,omitempty"`
	IsBot       bool   `json:"is_bot,omitempty"`
}

// Usergroup is a mention group.
type Usergroup struct {
	ID     string `json:"id"`
	Handle string `json:"handle,omitempty"`
	Name   string `json:"name,omitempty"`
}

// Edit is the last rewrite of a message.
type Edit struct {
	TS   string `json:"ts,omitempty"`
	User string `json:"user,omitempty"`
}

// Message is one message, as history, a thread or a search returns it.
type Message struct {
	TS       string `json:"ts"`
	User     string `json:"user,omitempty"`
	BotID    string `json:"bot_id,omitempty"`
	Username string `json:"username,omitempty"`
	Text     string `json:"text,omitempty"`
	Subtype  string `json:"subtype,omitempty"`
	ThreadTS string `json:"thread_ts,omitempty"`
	// ReplyCount is a pointer because Slack omits it for a message nobody answered,
	// and "no thread" is not the same as "a thread of zero".
	ReplyCount  *int   `json:"reply_count,omitempty"`
	LatestReply string `json:"latest_reply,omitempty"`
	Edited      *Edit  `json:"edited,omitempty"`
	Files       []File `json:"files,omitempty"`
}

// HistoryPage is one page of a conversation, with the cursor that continues it.
type HistoryPage struct {
	Messages   []Message
	HasMore    bool
	NextCursor string
}

// SearchMatch is one hit of Slack's own search.
type SearchMatch struct {
	TS        string `json:"ts"`
	Text      string `json:"text,omitempty"`
	User      string `json:"user,omitempty"`
	Username  string `json:"username,omitempty"`
	ThreadTS  string `json:"thread_ts,omitempty"`
	Permalink string `json:"permalink,omitempty"`
	Channel   *struct {
		ID   string `json:"id,omitempty"`
		Name string `json:"name,omitempty"`
	} `json:"channel,omitempty"`
}

// SearchPage is one page of Slack's own search.
type SearchPage struct {
	Total   int
	Matches []SearchMatch
}

// Posted is what a successful post reports.
type Posted struct {
	TS      string
	Channel string
	Text    string
}

// Upload is one attachment on its way up.
type Upload struct {
	Name  string
	Bytes []byte
}

// Uploaded is what an upload reports: the message it created, when Slack says,
// and the files it carries.
type Uploaded struct {
	TS      string
	FileIDs []string
}

// Options configure a client. The zero value is the production one: the real
// base URL, the real clock, the default pacing.
type Options struct {
	BaseURL string
	HTTP    *http.Client
	// Sleep is the clock waits go through, so a test can assert the backoff
	// without waiting for it.
	Sleep      func(time.Duration)
	MaxRetries int
	// Pace is the sustained gap between requests; nil means the default, which is why
	// it is a pointer: zero is a value a caller may want to ask for.
	Pace  *time.Duration
	Burst int
}

// Client talks to one workspace.
type Client struct {
	account auth.Account
	base    string
	http    *http.Client
	sleep   func(time.Duration)

	maxRetries int
	pace       time.Duration
	burst      int

	mu         sync.Mutex
	tokens     int
	refilledAt time.Time
	userNames  map[string]string
	channels   []Channel
	listed     bool
}

// NewClient builds a client for one stored session.
func NewClient(account auth.Account, options Options) *Client {
	base := options.BaseURL
	if base == "" {
		base = baseFromEnv()
	}

	if !strings.HasSuffix(base, "/") {
		base += "/"
	}

	client := options.HTTP
	if client == nil {
		client = &http.Client{Timeout: APITimeout}
	}

	sleep := options.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	maxRetries := options.MaxRetries
	if maxRetries == 0 {
		maxRetries = defaultRetries
	}

	pace := paceFromEnv()
	if options.Pace != nil {
		pace = *options.Pace
	}

	burst := options.Burst
	if burst == 0 {
		burst = defaultBurst
	}

	return &Client{
		account:    account,
		base:       base,
		http:       client,
		sleep:      sleep,
		maxRetries: maxRetries,
		pace:       pace,
		burst:      max(1, burst),
		tokens:     max(1, burst),
		refilledAt: time.Now(),
		userNames:  make(map[string]string),
	}
}

// WorkspaceName is the team the session belongs to, for output that has no room
// to ask Slack again.
func (c *Client) WorkspaceName() string { return c.account.TeamName }

// paceFromEnv is the sustained gap between requests, which a bulk sync needs and a
// single-call tool does not feel: SLACK_BROWSER_PACE_MS=0 turns it off.
func paceFromEnv() time.Duration {
	raw := strings.TrimSpace(envValue("SLACK_BROWSER_PACE_MS"))
	if raw == "" {
		return defaultPace
	}

	milliseconds, err := strconv.ParseFloat(raw, 64)
	if err != nil || milliseconds < 0 {
		return defaultPace
	}

	return time.Duration(milliseconds * float64(time.Millisecond))
}

func baseFromEnv() string {
	if value := strings.TrimSpace(envValue("SLACK_BROWSER_API_BASE")); value != "" {
		return value
	}

	return defaultAPIBase
}

// pace is a token bucket in front of every request. Slack documents its read
// limits as "50+ per minute", so a bulk sync pacing at one call every 500 ms
// stays under that while the bucket's burst keeps single-call tools instant.
func (c *Client) paceWait() {
	if c.pace <= 0 {
		return
	}

	now := time.Now()

	c.mu.Lock()

	gained := int(now.Sub(c.refilledAt) / c.pace)
	if gained > 0 {
		c.tokens = min(c.burst, c.tokens+gained)

		if c.tokens >= c.burst {
			c.refilledAt = now
		} else {
			c.refilledAt = c.refilledAt.Add(time.Duration(gained) * c.pace)
		}
	}

	if c.tokens > 0 {
		c.tokens--
		c.mu.Unlock()

		return
	}

	wait := c.refilledAt.Add(c.pace).Sub(now) + jitter(c.pace)
	c.mu.Unlock()

	// Clamped: a backward clock jump would otherwise turn the wait into a hang.
	c.sleep(max(0, min(wait, c.pace*4)))

	c.mu.Lock()
	c.refilledAt = time.Now()
	c.tokens = 0
	c.mu.Unlock()
}

// jitter is ±20%, so two machines syncing on the same schedule do not stay in
// lockstep.
func jitter(interval time.Duration) time.Duration {
	//nolint:gosec // spreading two machines' pacing apart needs no unpredictability
	return time.Duration(math.Round((rand.Float64()*2 - 1) * float64(interval) * 0.2))
}

func backoff(attempt int) time.Duration {
	return min(400*time.Millisecond*time.Duration(math.Pow(2, float64(attempt))), 4*time.Second)
}

func retryAfter(headers http.Header) (time.Duration, bool) {
	header := headers.Get("retry-after")
	if header == "" {
		return 0, false
	}

	seconds, err := strconv.ParseFloat(header, 64)
	if err != nil {
		return 0, false
	}

	return time.Duration(seconds * float64(time.Second)), true
}

// call posts one API method and answers the body it returned, once the envelope
// says the call worked. Pacing, the retry on a rate limit and the mapping from
// a Slack error code to a Go error all happen here so no method repeats them.
func (c *Client) call(method string, params map[string]any) ([]byte, error) {
	body, cookieName := c.encode(params)

	for attempt := 0; ; attempt++ {
		c.paceWait()

		raw, status, header, err := c.roundTrip(method, body, cookieName)
		if err != nil {
			return nil, err
		}

		if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
			if attempt >= c.maxRetries {
				return nil, &failure.APIError{
					Method: method,
					Code:   "http_" + strconv.Itoa(status),
					Detail: "gave up after retries",
				}
			}

			c.sleep(waitFor(header, attempt))

			continue
		}

		envelope, err := decodeEnvelope(method, status, raw)
		if err != nil {
			return nil, err
		}

		if envelope.OK {
			return raw, nil
		}

		code := envelope.Error
		if code == "" {
			code = "unknown_error"
		}

		if reauthErrors[code] {
			return nil, &failure.AuthError{
				Message: "Slack rejected the stored session for " + c.account.TeamName + " (" + code + ").",
				Hint:    "Run `slack-browser login` to sign in again.",
			}
		}

		if retryableErrors[code] && attempt < c.maxRetries {
			c.sleep(backoff(attempt))

			continue
		}

		return nil, &failure.APIError{Method: method, Code: code, Detail: envelope.Detail}
	}
}

func (c *Client) encode(params map[string]any) (string, string) {
	form := url.Values{}
	form.Set("token", c.account.Token)

	for key, value := range params {
		if value == nil {
			continue
		}

		form.Set(key, fmtValue(value))
	}

	cookieName := c.account.DCookieName
	if cookieName == "" {
		cookieName = "d"
	}

	return form.Encode(), cookieName
}

// roundTrip posts one request and answers what came back, body included and
// closed. The status is returned rather than interpreted: a rate limit is a
// retry, not a failure.
func (c *Client) roundTrip(method, body, cookieName string) ([]byte, int, http.Header, error) {
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, c.base+method, strings.NewReader(body))
	if err != nil {
		return nil, 0, nil, &failure.APIError{Method: method, Code: codeBadRequest, Detail: redact.Text(err.Error())}
	}

	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Cookie", cookieName+"="+c.account.DCookie)

	response, err := c.http.Do(request)
	if err != nil {
		return nil, 0, nil, &failure.APIError{Method: method, Code: codeNetwork, Detail: redact.Text(err.Error())}
	}

	raw, err := io.ReadAll(response.Body)
	_ = response.Body.Close()

	if err != nil {
		return nil, 0, nil, &failure.APIError{Method: method, Code: codeNetwork, Detail: redact.Text(err.Error())}
	}

	return raw, response.StatusCode, response.Header, nil
}

type envelope struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error"`
	Detail string `json:"detail"`
}

func decodeEnvelope(method string, status int, raw []byte) (envelope, error) {
	var decoded envelope
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return envelope{}, &failure.APIError{
			Method: method,
			Code:   "invalid_json_" + strconv.Itoa(status),
			Detail: redact.Text(first(raw, 160)),
		}
	}

	return decoded, nil
}

func waitFor(header http.Header, attempt int) time.Duration {
	if wait, known := retryAfter(header); known {
		return wait
	}

	return backoff(attempt)
}

// callInto is call with the body decoded into a payload struct.
func callInto[T any](c *Client, method string, params map[string]any) (T, error) {
	var zero T

	raw, err := c.call(method, params)
	if err != nil {
		return zero, err
	}

	var payload T
	if err := json.Unmarshal(raw, &payload); err != nil {
		return zero, &failure.APIError{Method: method, Code: "invalid_payload", Detail: redact.Text(err.Error())}
	}

	return payload, nil
}

// AuthTest asks Slack who this session is.
func (c *Client) AuthTest() (AuthInfo, error) {
	payload, err := callInto[struct {
		Team   string `json:"team"`
		TeamID string `json:"team_id"`
		User   string `json:"user"`
		UserID string `json:"user_id"`
		URL    string `json:"url"`
	}](c, "auth.test", nil)
	if err != nil {
		return AuthInfo{}, err
	}

	return AuthInfo{
		Team:   payload.Team,
		TeamID: payload.TeamID,
		User:   payload.User,
		UserID: payload.UserID,
		URL:    payload.URL,
	}, nil
}

type metadata struct {
	NextCursor string `json:"next_cursor"`
}

// ListMyChannels is every conversation this session is in, cached for the life
// of the client because a read resolves one name and then pages a history.
func (c *Client) ListMyChannels() ([]Channel, error) {
	c.mu.Lock()

	if c.listed {
		channels := c.channels
		c.mu.Unlock()

		return channels, nil
	}

	c.mu.Unlock()

	collected := make([]Channel, 0, 64)

	cursor := ""

	for range 20 {
		params := map[string]any{
			"types":            "public_channel,private_channel,mpim,im",
			"exclude_archived": true,
			"limit":            200,
		}

		if cursor != "" {
			params["cursor"] = cursor
		}

		payload, err := callInto[struct {
			Channels []Channel `json:"channels"`
			Metadata metadata  `json:"response_metadata"`
		}](c, "users.conversations", params)
		if err != nil {
			return nil, err
		}

		collected = append(collected, payload.Channels...)

		cursor = payload.Metadata.NextCursor
		if cursor == "" {
			break
		}
	}

	c.mu.Lock()
	c.channels = collected
	c.listed = true
	c.mu.Unlock()

	return collected, nil
}

// ListDMChannels is the IMs and group DMs, which a channel listing never returns.
func (c *Client) ListDMChannels() ([]Channel, error) {
	channels, err := c.ListMyChannels()
	if err != nil {
		return nil, err
	}

	direct := make([]Channel, 0, len(channels))

	for _, channel := range channels {
		if channel.IsIM || channel.IsMpIM {
			direct = append(direct, channel)
		}
	}

	return direct, nil
}

// PersonLookup answers a person reference (`@ana`, `dm:Ana Ruiz`) with a user id,
// so a DM can be addressed the way a person names it. A client holds no
// directory, so the caller supplies it.
type PersonLookup func(reference string) (string, bool)

// ResolveChannel turns what a person typed into a conversation.
func (c *Client) ResolveChannel(reference string, person PersonLookup) (Channel, error) {
	printed := ParsePrintedReference(reference)

	// No name: a DM has none, and filing one would put an id in channel_name.
	if printed.ID != "" {
		return Channel{ID: printed.ID}, nil
	}

	raw := strings.TrimPrefix(printed.Reference, "#")
	if conversationIDPattern.MatchString(raw) {
		return Channel{ID: raw}, nil
	}

	switch strings.ToLower(raw) {
	case "me", "@me", "self", "dm:self":
		return c.openSelfDM()
	}

	channels, err := c.ListMyChannels()
	if err != nil {
		return Channel{}, err
	}

	matches := matchChannels(channels, strings.ToLower(raw))

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		if resolved, ok := c.resolvePerson(reference, channels, person); ok {
			return resolved, nil
		}

		return Channel{}, &failure.ChannelError{Message: "No conversation named \"" + raw + "\" in " +
			c.account.TeamName + ". Use list_conversations to see the available names."}
	}

	return Channel{}, &failure.ChannelError{Message: "\"" + raw + "\" matches several conversations: " +
		ambiguousChannels(matches) + "."}
}

// matchChannels prefers an exact name over a partial one, and answers every
// candidate so the caller can tell one match from many.
func matchChannels(channels []Channel, lowered string) []Channel {
	var exact, partial []Channel

	for _, channel := range channels {
		if channel.Name == "" {
			continue
		}

		name := strings.ToLower(channel.Name)
		if name == lowered {
			exact = append(exact, channel)
		}

		if strings.Contains(name, lowered) {
			partial = append(partial, channel)
		}
	}

	if len(exact) > 0 {
		return exact
	}

	return partial
}

func ambiguousChannels(matches []Channel) string {
	candidates := make([]string, 0, 8)

	for _, channel := range matches {
		if len(candidates) == 8 {
			break
		}

		prefix := "#"
		if channel.IsPrivate {
			prefix = "private:"
		}

		candidates = append(candidates, prefix+channel.Name+" ("+channel.ID+")")
	}

	return strings.Join(candidates, ", ")
}

func (c *Client) resolvePerson(reference string, channels []Channel, person PersonLookup) (Channel, bool) {
	if person == nil {
		return Channel{}, false
	}

	name := strings.TrimSpace(reference)
	name = strings.TrimPrefix(name, "#")
	name = strings.TrimPrefix(name, "dm:")
	name = strings.TrimPrefix(name, "@")

	if name == "" {
		return Channel{}, false
	}

	userID, ok := person(name)
	if !ok || userID == "" {
		return Channel{}, false
	}

	for _, channel := range channels {
		if channel.IsIM && channel.User == userID {
			return channel, true
		}
	}

	return Channel{}, false
}

// History is one page of a channel, newest first as Slack serves it.
func (c *Client) History(params HistoryParams) (HistoryPage, error) {
	form := map[string]any{
		paramChannel: params.Channel,
		paramLimit:   params.Limit,
	}

	if params.Limit == 0 {
		form["limit"] = 20
	}

	if params.Oldest != "" {
		form["oldest"] = params.Oldest
	}

	if params.Latest != "" {
		form["latest"] = params.Latest
	}

	if params.Cursor != "" {
		form["cursor"] = params.Cursor
	}

	payload, err := callInto[struct {
		Messages []Message `json:"messages"`
		HasMore  bool      `json:"has_more"`
		Metadata metadata  `json:"response_metadata"`
	}](c, "conversations.history", form)
	if err != nil {
		return HistoryPage{}, err
	}

	return HistoryPage{
		Messages:   payload.Messages,
		HasMore:    payload.HasMore,
		NextCursor: payload.Metadata.NextCursor,
	}, nil
}

// HistoryParams is what one page of a conversation is asked for.
type HistoryParams struct {
	Channel string
	Limit   int
	Oldest  string
	Latest  string
	Cursor  string
}

// DeleteMessage removes the author's own message: Slack refuses anybody else's.
func (c *Client) DeleteMessage(channel, ts string) error {
	_, err := c.call("chat.delete", map[string]any{"channel": channel, "ts": ts})

	return err
}

// RepliesPage is one page of a thread. `conversations.replies` caps a page at
// 200, so a busy thread has to be walked with the cursor or it is filed short.
func (c *Client) RepliesPage(channel, threadTS string, limit int, cursor string) (HistoryPage, error) {
	form := map[string]any{
		"channel": channel,
		"ts":      threadTS,
	}

	if limit == 0 {
		form["limit"] = 100
	} else {
		form["limit"] = limit
	}

	if cursor != "" {
		form["cursor"] = cursor
	}

	payload, err := callInto[struct {
		Messages []Message `json:"messages"`
		HasMore  bool      `json:"has_more"`
		Metadata metadata  `json:"response_metadata"`
	}](c, "conversations.replies", form)
	if err != nil {
		return HistoryPage{}, err
	}

	return HistoryPage{
		Messages:   payload.Messages,
		HasMore:    payload.HasMore,
		NextCursor: payload.Metadata.NextCursor,
	}, nil
}

// Replies is the first page of a thread, which is what `thread` shows.
func (c *Client) Replies(channel, threadTS string) ([]Message, error) {
	page, err := c.RepliesPage(channel, threadTS, 0, "")
	if err != nil {
		return nil, err
	}

	return page.Messages, nil
}

// SearchMessages is Slack's own search, for what the local index cannot hold: a
// message older than the backfill window, or a channel never synced here.
func (c *Client) SearchMessages(query string, count, page int) (SearchPage, error) {
	form := map[string]any{"query": query, "count": count}
	if count == 0 {
		form["count"] = 20
	}

	if page > 1 {
		form["page"] = page
	}

	payload, err := callInto[struct {
		Messages struct {
			Total   int           `json:"total"`
			Matches []SearchMatch `json:"matches"`
		} `json:"messages"`
	}](c, "search.messages", form)
	if err != nil {
		return SearchPage{}, err
	}

	return SearchPage{Total: payload.Messages.Total, Matches: payload.Messages.Matches}, nil
}

// PostMessage writes one message and answers where it landed.
func (c *Client) PostMessage(channel, text, threadTS string, blocks any) (Posted, error) {
	form := map[string]any{"channel": channel, "text": text}

	if threadTS != "" {
		form["thread_ts"] = threadTS
	}

	if blocks != nil {
		encoded, err := json.Marshal(blocks)
		if err != nil {
			return Posted{}, err
		}

		form["blocks"] = string(encoded)
	}

	payload, err := callInto[struct {
		TS      string `json:"ts"`
		Channel string `json:"channel"`
		Message struct {
			Text string `json:"text"`
		} `json:"message"`
	}](c, "chat.postMessage", form)
	if err != nil {
		return Posted{}, err
	}

	return Posted{TS: payload.TS, Channel: payload.Channel, Text: payload.Message.Text}, nil
}

// UploadFiles sends attachments and, in the same message, their comment. Slack
// builds the message from the upload, so the comment and the files share one
// `ts`; posting first and uploading after would be two messages.
//
// The pre-signed URL is posted to without credentials: Slack signs it, so the
// xoxc token and the `d` cookie never leave `slack.com/api`.
func (c *Client) UploadFiles(channel string, files []Upload, text, threadTS string) (Uploaded, error) {
	described := make([]map[string]string, 0, len(files))

	for _, file := range files {
		target, err := callInto[struct {
			URL    string `json:"upload_url"`
			FileID string `json:"file_id"`
		}](c, "files.getUploadURLExternal", map[string]any{"filename": file.Name, "length": len(file.Bytes)})
		if err != nil {
			return Uploaded{}, err
		}

		if err := c.putBytes(target.URL, file); err != nil {
			return Uploaded{}, err
		}

		described = append(described, map[string]string{"id": target.FileID, "title": file.Name})
	}

	encoded, err := json.Marshal(described)
	if err != nil {
		return Uploaded{}, err
	}

	form := map[string]any{"files": string(encoded), "channel_id": channel}

	if text != "" {
		form["initial_comment"] = text
	}

	if threadTS != "" {
		form["thread_ts"] = threadTS
	}

	payload, err := callInto[struct {
		Files []File `json:"files"`
	}](c, "files.completeUploadExternal", form)
	if err != nil {
		return Uploaded{}, err
	}

	ids := make([]string, 0, len(payload.Files))
	ts := ""

	for _, file := range payload.Files {
		if file.ID != "" {
			ids = append(ids, file.ID)
		}

		if ts == "" {
			ts = sharedTS(file.Shares)
		}
	}

	// `completeUploadExternal` describes where the upload landed but is not
	// obliged to: when the shares come back empty, `files.info` still reports the
	// message the upload created.
	if ts == "" {
		ts = c.sharedTSFromInfo(ids)
	}

	return Uploaded{TS: ts, FileIDs: ids}, nil
}

// sharedTSFromInfo looks three times, about a second in total: Slack files the
// share a beat after the upload completes, so one look can answer "no shares
// yet" for a message already in the channel. An upload that never reports one is
// still a sent message, so this answers nothing rather than failing.
func (c *Client) sharedTSFromInfo(fileIDs []string) string {
	waits := []time.Duration{120 * time.Millisecond, 350 * time.Millisecond, 800 * time.Millisecond}

	for attempt := 0; ; attempt++ {
		for _, fileID := range fileIDs {
			file, err := c.FileInfo(fileID)
			if err != nil {
				continue
			}

			if ts := sharedTS(file.Shares); ts != "" {
				return ts
			}
		}

		if attempt >= len(waits) {
			return ""
		}

		c.sleep(waits[attempt])
	}
}

func (c *Client) putBytes(target string, file Upload) error {
	var body bytes.Buffer

	form := multipart.NewWriter(&body)

	if err := form.WriteField("filename", file.Name); err != nil {
		return &failure.APIError{Method: methodUpload, Code: codeBadRequest, Detail: file.Name}
	}

	part, err := form.CreateFormFile("file", file.Name)
	if err != nil {
		return &failure.APIError{Method: methodUpload, Code: codeBadRequest, Detail: file.Name}
	}

	if _, err := part.Write(file.Bytes); err != nil {
		return &failure.APIError{Method: methodUpload, Code: codeBadRequest, Detail: file.Name}
	}

	if err := form.Close(); err != nil {
		return &failure.APIError{Method: methodUpload, Code: codeBadRequest, Detail: file.Name}
	}

	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, target, &body)
	if err != nil {
		return &failure.APIError{Method: methodUpload, Code: codeBadRequest, Detail: file.Name}
	}

	request.Header.Set("Content-Type", form.FormDataContentType())

	response, err := c.http.Do(request)
	if err != nil {
		return &failure.APIError{Method: methodUpload, Code: codeNetwork, Detail: file.Name}
	}

	defer func() { _ = response.Body.Close() }()

	if response.StatusCode/100 != 2 {
		return &failure.APIError{Method: methodUpload, Code: "http_" + strconv.Itoa(response.StatusCode), Detail: file.Name}
	}

	return nil
}

func (c *Client) openSelfDM() (Channel, error) {
	if c.account.UserID == "" {
		return Channel{}, &failure.AuthError{
			Message: "The stored session for " + c.account.TeamName + " has no user id; re-run `slack-browser login`.",
			Hint:    "Run `slack-browser login` to sign in again.",
		}
	}

	payload, err := callInto[struct {
		Channel struct {
			ID string `json:"id"`
		} `json:"channel"`
	}](c, "conversations.open", map[string]any{"users": c.account.UserID})
	if err != nil {
		return Channel{}, err
	}

	if payload.Channel.ID == "" {
		return Channel{}, errors.New("conversations.open returned no channel id")
	}

	return Channel{ID: payload.Channel.ID, Name: "self-dm", IsIM: true, User: c.account.UserID}, nil
}

// ListUsers is every workspace member, for the directory.
func (c *Client) ListUsers() ([]User, error) {
	collected := make([]User, 0, 256)

	cursor := ""

	for range 20 {
		params := map[string]any{"limit": 200}
		if cursor != "" {
			params["cursor"] = cursor
		}

		payload, err := callInto[struct {
			Members []struct {
				User
				Profile struct {
					DisplayName string `json:"display_name"`
				} `json:"profile"`
			} `json:"members"`
			Metadata metadata `json:"response_metadata"`
		}](c, "users.list", params)
		if err != nil {
			return nil, err
		}

		for _, member := range payload.Members {
			user := member.User
			user.DisplayName = member.Profile.DisplayName
			collected = append(collected, user)
		}

		cursor = payload.Metadata.NextCursor
		if cursor == "" {
			break
		}
	}

	return collected, nil
}

// ListAllChannels is every public and private channel, for the directory.
func (c *Client) ListAllChannels() ([]Channel, error) {
	collected := make([]Channel, 0, 256)

	cursor := ""

	for range 20 {
		params := map[string]any{
			"types":            "public_channel,private_channel",
			"exclude_archived": true,
			"limit":            200,
		}

		if cursor != "" {
			params["cursor"] = cursor
		}

		payload, err := callInto[struct {
			Channels []Channel `json:"channels"`
			Metadata metadata  `json:"response_metadata"`
		}](c, "conversations.list", params)
		if err != nil {
			return nil, err
		}

		collected = append(collected, payload.Channels...)

		cursor = payload.Metadata.NextCursor
		if cursor == "" {
			break
		}
	}

	return collected, nil
}

// ListUsergroups is every mention group.
func (c *Client) ListUsergroups() ([]Usergroup, error) {
	payload, err := callInto[struct {
		Usergroups []Usergroup `json:"usergroups"`
	}](c, "usergroups.list", nil)
	if err != nil {
		return nil, err
	}

	return payload.Usergroups, nil
}

// FileInfo describes one file.
func (c *Client) FileInfo(fileID string) (File, error) {
	payload, err := callInto[struct {
		File *File `json:"file"`
	}](c, "files.info", map[string]any{"file": fileID})
	if err != nil {
		return File{}, err
	}

	if payload.File == nil {
		return File{}, errors.New("files.info returned no file for " + fileID)
	}

	return *payload.File, nil
}

// DownloadFile is the bytes of a file a message carries. History gives the URL
// in the common case; when it does not, `files.info` does.
func (c *Client) DownloadFile(file File) ([]byte, error) {
	resolved := file

	if file.URLPrivate == "" && file.URLPrivateDownload == "" {
		fetched, err := c.FileInfo(file.ID)
		if err != nil {
			return nil, err
		}

		resolved = fetched
	}

	target := resolved.URLPrivate
	if target == "" {
		target = resolved.URLPrivateDownload
	}

	if target == "" {
		return nil, errors.New("no download URL for file " + resolved.ID)
	}

	body, _, err := c.FetchAttachment(target)

	return body, err
}

// FetchAttachment is the bytes behind a Slack URL, with the credentials the URL
// needs. Slack redirects between its own hosts, which keeps the header on the
// request.
func (c *Client) FetchAttachment(target string) ([]byte, string, error) {
	cookieName := c.account.DCookieName
	if cookieName == "" {
		cookieName = "d"
	}

	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if err != nil {
		return nil, "", &failure.APIError{Method: methodDownload, Code: codeBadRequest}
	}

	request.Header.Set("Authorization", "Bearer "+c.account.Token)
	request.Header.Set("Cookie", cookieName+"="+c.account.DCookie)

	response, err := c.http.Do(request)
	if err != nil {
		return nil, "", &failure.APIError{Method: methodDownload, Code: codeNetwork}
	}

	defer func() { _ = response.Body.Close() }()

	if response.StatusCode/100 != 2 {
		return nil, "", &failure.APIError{Method: methodDownload, Code: "http_" + strconv.Itoa(response.StatusCode)}
	}

	body, err := readAll(response)
	if err != nil {
		return nil, "", &failure.APIError{Method: methodDownload, Code: codeNetwork}
	}

	return body, response.Header.Get("content-type"), nil
}

// NamesFor resolves the authors of a page. Without a directory every author
// costs a `users.info`, and a 200-message page would turn one read into 200
// requests, so past the cap the id is shown.
func (c *Client) NamesFor(messages []Message, known func(string) (string, bool)) map[string]string {
	names := make(map[string]string, len(messages))
	lookups := 0

	for _, message := range messages {
		if message.User == "" {
			continue
		}

		if _, done := names[message.User]; done {
			continue
		}

		if known != nil {
			if resolved, ok := known(message.User); ok && resolved != "" {
				names[message.User] = resolved

				continue
			}
		}

		if lookups >= maxNameLookups {
			continue
		}

		lookups++

		names[message.User] = c.DisplayName(message.User)
	}

	return names
}

// DisplayName is what a person is called, or the id when Slack will not say.
func (c *Client) DisplayName(userID string) string {
	if userID == "" {
		return "unknown"
	}

	c.mu.Lock()
	cached, ok := c.userNames[userID]
	c.mu.Unlock()

	if ok {
		return cached
	}

	payload, err := callInto[struct {
		User struct {
			Name     string `json:"name"`
			RealName string `json:"real_name"`
			Profile  struct {
				DisplayName string `json:"display_name"`
			} `json:"profile"`
		} `json:"user"`
	}](c, "users.info", map[string]any{"user": userID})
	if err != nil {
		return userID
	}

	name := payload.User.Profile.DisplayName
	if name == "" {
		name = payload.User.RealName
	}

	if name == "" {
		name = payload.User.Name
	}

	if name == "" {
		name = userID
	}

	c.mu.Lock()
	c.userNames[userID] = name
	c.mu.Unlock()

	return name
}

// sharedTS is the message a file's shares point at, from any scope or channel.
func sharedTS(shares map[string]any) string {
	for _, scope := range shares {
		entries, ok := scope.(map[string]any)
		if !ok {
			continue
		}

		for _, value := range entries {
			list, ok := value.([]any)
			if !ok || len(list) == 0 {
				continue
			}

			if entry, ok := list[0].(map[string]any); ok {
				if ts, ok := entry["ts"].(string); ok && ts != "" {
					return ts
				}
			}
		}
	}

	return ""
}

var conversationIDPattern = regexp.MustCompile(`^[CGD][A-Z0-9]{6,}$`)

func envValue(name string) string {
	return os.Getenv(name)
}

func readAll(response *http.Response) ([]byte, error) {
	defer func() { _ = response.Body.Close() }()

	return io.ReadAll(response.Body)
}

// first is the head of a body kept for an error message, in characters rather
// than bytes so a truncated multi-byte character does not become a replacement.
func first(body []byte, chars int) string {
	runes := []rune(string(body))
	if len(runes) <= chars {
		return string(runes)
	}

	return string(runes[:chars])
}

func fmtValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}
