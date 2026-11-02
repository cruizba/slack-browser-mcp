// Package fakeapi serves recorded Slack Web API replies so two builds of the
// client can be driven against the same server and diffed.
//
// The fixtures are real captures, not invented shapes: a fake that guesses the
// response format tests the guess, not the client. Identifiers were replaced
// with stable stand-ins and any URL that pointed at slack.com carries
// [BasePlaceholder] instead.
package fakeapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// BasePlaceholder is where a fixture points back at the server's own address.
const BasePlaceholder = "{{BASE}}"

// keyError is the JSON field Slack uses to name a failure.
const keyError = "error"

// Reply is one recorded response: the status the real call returned, plus the
// body verbatim.
type Reply struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

// Server answers `/api/<method>` from recorded replies and records what it was
// asked for, so a caller can assert the requests as well as the responses.
type Server struct {
	replies map[string]Reply
	log     *slog.Logger

	mu       sync.Mutex
	injected map[string]int
	calls    []string
}

// New loads every `<method>.json` in dir. A directory with no replies is an
// error: a server that silently 404s every call hides the mistake.
func New(dir string, log *slog.Logger) (*Server, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("listing fixtures in %s: %w", dir, err)
	}

	if len(paths) == 0 {
		return nil, fmt.Errorf("no fixtures in %s", dir)
	}

	replies := make(map[string]Reply, len(paths))

	for _, path := range paths {
		raw, err := os.ReadFile(path) //nolint:gosec // the directory is an operator flag, never request data
		if err != nil {
			return nil, fmt.Errorf("reading fixture %s: %w", path, err)
		}

		var reply Reply
		if err := json.Unmarshal(raw, &reply); err != nil {
			return nil, fmt.Errorf("parsing fixture %s: %w", path, err)
		}

		method := strings.TrimSuffix(filepath.Base(path), ".json")
		replies[method] = reply
	}

	return &Server{
		replies:  replies,
		log:      log,
		injected: map[string]int{},
		calls:    []string{},
	}, nil
}

// Fail makes the next n calls to method answer 429 with retry-after: 0, which
// is how the pacing and retry paths get exercised without a real rate limit.
func (s *Server) Fail(method string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.injected[method] += n
}

// Calls returns the methods served, in order.
func (s *Server) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string{}, s.calls...)
}

// Handler serves the recorded replies.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/{method}", s.serveMethod)

	return mux
}

func (s *Server) serveMethod(w http.ResponseWriter, r *http.Request) {
	method := r.PathValue("method")

	if err := r.ParseForm(); err != nil {
		s.reply(w, http.StatusOK, map[string]any{"ok": false, keyError: "invalid_form"})

		return
	}

	s.mu.Lock()

	s.calls = append(s.calls, method)

	remaining := s.injected[method]
	if remaining > 0 {
		s.injected[method] = remaining - 1
	}
	s.mu.Unlock()

	if remaining > 0 {
		w.Header().Set("Retry-After", "0")
		s.reply(w, http.StatusTooManyRequests, map[string]any{"ok": false, keyError: "ratelimited"})

		return
	}

	// The real API answers 200 with ok:false when the credentials are missing,
	// so a client that forgets either half fails here rather than in Slack.
	if r.PostForm.Get("token") == "" || len(r.Cookies()) == 0 {
		s.reply(w, http.StatusOK, map[string]any{"ok": false, keyError: "not_authed"})

		return
	}

	recorded, ok := s.replies[method]
	if !ok {
		s.log.Warn("no recorded reply", "method", method)
		s.reply(w, http.StatusOK, map[string]any{"ok": false, keyError: "unknown_method"})

		return
	}

	s.log.Debug("serving", "method", method, "status", recorded.Status)
	s.writeRaw(w, recorded.Status, recorded.Body, r.Host)
}

func (s *Server) reply(w http.ResponseWriter, status int, body any) {
	raw, err := json.Marshal(body)
	if err != nil {
		http.Error(w, `{"ok":false,"error":"encoding_failed"}`, http.StatusInternalServerError)

		return
	}

	s.writeRaw(w, status, raw, "")
}

// writeRaw points every reply back at this server, so an absolute URL recorded
// from the real workspace cannot leak a request to slack.com during a test.
func (s *Server) writeRaw(w http.ResponseWriter, status int, body []byte, host string) {
	if host != "" {
		scheme := "http://"
		body = []byte(strings.ReplaceAll(string(body), BasePlaceholder, scheme+host))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if _, err := w.Write(body); err != nil && !errors.Is(err, http.ErrHandlerTimeout) { //nolint:gosec // JSON for an API client, not an HTML document
		s.log.Debug("writing reply", "err", err)
	}
}
