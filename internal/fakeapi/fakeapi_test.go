package fakeapi_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/fakeapi"
)

type response struct {
	status int
	header http.Header
	body   []byte
}

func fixturesDir() string {
	return filepath.Join("..", "..", "testdata", "live")
}

func newServer(t *testing.T) *fakeapi.Server {
	t.Helper()

	log := slog.New(slog.DiscardHandler)

	server, err := fakeapi.New(fixturesDir(), log)
	if err != nil {
		t.Fatalf("fakeapi.New: %v", err)
	}

	return server
}

func newHTTP(t *testing.T, server *fakeapi.Server) *httptest.Server {
	t.Helper()

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	return httpServer
}

func post(t *testing.T, base, method string, authenticated bool) response {
	t.Helper()

	form := ""
	if authenticated {
		form = "token=xoxc-test"
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/api/"+method, strings.NewReader(form))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if authenticated {
		req.Header.Set("Cookie", "d=xoxd-test")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", method, err)
	}

	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Logf("closing %s body: %v", method, cerr)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s body: %v", method, err)
	}

	return response{status: resp.StatusCode, header: resp.Header, body: body}
}

func TestServer_Handler_ServesEveryRecordedMethod(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(fixturesDir())
	if err != nil {
		t.Fatalf("reading fixtures: %v", err)
	}

	httpServer := newHTTP(t, newServer(t))

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		method := strings.TrimSuffix(entry.Name(), ".json")

		got := post(t, httpServer.URL, method, true)

		if got.status != http.StatusOK {
			t.Errorf("%s: status %d, want 200", method, got.status)
		}

		var envelope struct {
			OK bool `json:"ok"`
		}

		if err := json.Unmarshal(got.body, &envelope); err != nil {
			t.Errorf("%s: body is not JSON: %v", method, err)

			continue
		}

		if !envelope.OK {
			t.Errorf("%s: ok is false: %s", method, got.body)
		}

		if strings.Contains(string(got.body), fakeapi.BasePlaceholder) {
			t.Errorf("%s: reply still points at the real Slack: %s", method, got.body)
		}
	}
}

func TestServer_Handler_ReportsAnUnknownMethod(t *testing.T) {
	t.Parallel()

	httpServer := newHTTP(t, newServer(t))

	got := post(t, httpServer.URL, "chat.nope", true)

	if got.status != http.StatusOK {
		t.Fatalf("status %d, want 200", got.status)
	}

	if !strings.Contains(string(got.body), "unknown_method") {
		t.Fatalf("body %s, want unknown_method", got.body)
	}
}

func TestServer_Handler_RefusesACallWithNoCredentials(t *testing.T) {
	t.Parallel()

	httpServer := newHTTP(t, newServer(t))

	got := post(t, httpServer.URL, "auth.test", false)

	if !strings.Contains(string(got.body), "not_authed") {
		t.Fatalf("body %s, want not_authed", got.body)
	}
}

func TestServer_Fail(t *testing.T) {
	t.Parallel()

	server := newServer(t)
	server.Fail("auth.test", 1)

	httpServer := newHTTP(t, server)

	first := post(t, httpServer.URL, "auth.test", true)

	if first.status != http.StatusTooManyRequests {
		t.Fatalf("first status %d, want 429", first.status)
	}

	if got := first.header.Get("Retry-After"); got != "0" {
		t.Fatalf("retry-after %q, want 0", got)
	}

	if !strings.Contains(string(first.body), "ratelimited") {
		t.Fatalf("first body %s, want ratelimited", first.body)
	}

	second := post(t, httpServer.URL, "auth.test", true)

	if second.status != http.StatusOK {
		t.Fatalf("second status %d, want 200", second.status)
	}

	var envelope struct {
		OK bool `json:"ok"`
	}

	if err := json.Unmarshal(second.body, &envelope); err != nil {
		t.Fatalf("second body is not JSON: %v", err)
	}

	if !envelope.OK {
		t.Fatalf("second body %s, want ok", second.body)
	}
}

func TestServer_Calls(t *testing.T) {
	t.Parallel()

	server := newServer(t)
	httpServer := newHTTP(t, server)

	post(t, httpServer.URL, "auth.test", true)
	post(t, httpServer.URL, "users.info", true)

	got := server.Calls()
	want := []string{"auth.test", "users.info"}

	if len(got) != len(want) {
		t.Fatalf("calls %v, want %v", got, want)
	}

	for i, method := range want {
		if got[i] != method {
			t.Fatalf("calls %v, want %v", got, want)
		}
	}
}
