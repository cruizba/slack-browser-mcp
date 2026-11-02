package update_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/cruizba/slack-browser-mcp/internal/update"
)

// releasesServer is a release list with one stable release and one asset per target.
func releasesServer(t *testing.T, checksumOf string) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("/releases", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")

		_, _ = response.Write([]byte(`[
			{"tag_name":"v0.8.0","prerelease":false,"assets":[
				{"name":"slack-browser_0.8.0_linux_amd64.tar.gz","browser_download_url":"` + checksumOf + `","size":10},
				{"name":"slack-browser_0.8.0_checksums.txt","browser_download_url":"` + checksumOf + `","size":10}
			]},
			{"tag_name":"v0.9.0-rc.1","prerelease":true,"assets":[]}
		]`))
	})

	return httptest.NewServer(mux)
}

func TestLatestSkipsPrereleases(t *testing.T) {
	t.Parallel()

	server := releasesServer(t, "http://example.invalid/x")
	defer server.Close()

	client := &update.Client{API: server.URL + "/releases", HTTP: server.Client()}

	release, err := client.Latest(context.Background(), false)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}

	if release.Tag != "v0.8.0" {
		t.Fatalf("Latest = %q, want the stable one", release.Tag)
	}
}

func TestAssetForPicksThisPlatform(t *testing.T) {
	t.Parallel()

	release := update.Release{Tag: "v0.8.0", Assets: []update.Asset{
		{Name: "slack-browser_0.8.0_darwin_arm64.tar.gz"},
		{Name: "slack-browser_0.8.0_linux_amd64.tar.gz"},
		{Name: "slack-browser_0.8.0_checksums.txt"},
	}}

	asset, ok := update.AssetFor(release, "linux", "amd64")
	if !ok || !strings.HasSuffix(asset.Name, "linux_amd64.tar.gz") {
		t.Fatalf("AssetFor = %+v, %v", asset, ok)
	}

	if _, ok := update.AssetFor(release, "windows", "amd64"); ok {
		t.Fatal("a platform with no build answered an asset")
	}
}

func TestFetchRefusesAWrongChecksum(t *testing.T) {
	t.Parallel()

	payload := []byte("not really a binary")

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "checksums.txt"):
			_, _ = response.Write([]byte(hex.EncodeToString(make([]byte, 32)) + "  slack-browser_0.8.0_linux_amd64.tar.gz\n"))
		default:
			_, _ = response.Write(payload)
		}
	}))
	defer server.Close()

	client := &update.Client{API: server.URL + "/releases", HTTP: server.Client()}

	release := update.Release{Tag: "v0.8.0", Assets: []update.Asset{
		{Name: "slack-browser_0.8.0_linux_amd64.tar.gz", URL: server.URL + "/asset"},
		{Name: "slack-browser_0.8.0_checksums.txt", URL: server.URL + "/checksums.txt"},
	}}

	digests, err := client.Checksums(context.Background(), release)
	if err != nil {
		t.Fatalf("Checksums: %v", err)
	}

	if _, err := client.Fetch(context.Background(), release, release.Assets[0], digests); err == nil {
		t.Fatal("a download that does not match its checksum was accepted")
	}

	// And the same download with the right digest is accepted.
	sum := sha256.Sum256(payload)
	digests[release.Assets[0].Name] = hex.EncodeToString(sum[:])

	if _, err := client.Fetch(context.Background(), release, release.Assets[0], digests); err != nil {
		t.Fatalf("a matching download was refused: %v", err)
	}
}

func TestReplaceSwapsTheFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := dir + "/slack-browser"

	if err := writeFile(target, "old"); err != nil {
		t.Fatalf("writing the old binary: %v", err)
	}

	if err := update.Replace(target, []byte("new")); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	body, err := readFile(target)
	if err != nil || body != "new" {
		t.Fatalf("after Replace the file holds %q, %v", body, err)
	}
}

func TestParseChecksums(t *testing.T) {
	t.Parallel()

	digests := update.ParseChecksums("abc123  one.tar.gz\ndef456 *two.tar.gz\nnonsense\n")

	if digests["one.tar.gz"] != "abc123" || digests["two.tar.gz"] != "def456" {
		t.Fatalf("ParseChecksums = %v", digests)
	}
}

func writeFile(path, contents string) error {
	return os.WriteFile(path, []byte(contents), 0o600)
}

func readFile(path string) (string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the path is this test's own temporary file

	return string(raw), err
}

// releasesServer answers the release list the way GitHub does, and records what the
// client sent so the two things the API needs — the accept header and the token when
// one is configured — are checked rather than assumed.
func releaseListServer(t *testing.T, body string) (*httptest.Server, *http.Header) {
	t.Helper()

	var seen http.Header

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		seen = request.Header.Clone()
		response.Header().Set("Content-Type", "application/json")

		_, _ = response.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	return server, &seen
}

func TestClientLatestPicksTheNewestStable(t *testing.T) {
	t.Parallel()

	// Listed newest-first, which is how GitHub answers and how a client that stops at
	// the first item would get the wrong answer when a prerelease is on top.
	body := `[
	  {"tag_name":"v2.0.0-rc.1","prerelease":true,"draft":false},
	  {"tag_name":"v1.9.0","prerelease":false,"draft":false},
	  {"tag_name":"v1.10.0","prerelease":false,"draft":false},
	  {"tag_name":"not-a-version","prerelease":false,"draft":false}
	]`

	server, headers := releaseListServer(t, body)

	client := &update.Client{API: server.URL, Token: "t0ken", HTTP: server.Client()}

	release, err := client.Latest(context.Background(), false)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}

	// 1.10.0 beats 1.9.0 by number, not by string: the two tags that differ widest
	// are the ones a lexical comparison gets wrong.
	if release.Tag != "v1.10.0" {
		t.Fatalf("Latest = %q, want v1.10.0", release.Tag)
	}

	if got := headers.Get("Accept"); got != "application/vnd.github+json" {
		t.Fatalf("Accept = %q", got)
	}

	if got := headers.Get("Authorization"); got != "Bearer t0ken" {
		t.Fatalf("Authorization = %q, want the token when one is configured", got)
	}

	withPrerelease, err := client.Latest(context.Background(), true)
	if err != nil {
		t.Fatalf("Latest(prerelease): %v", err)
	}

	if withPrerelease.Tag != "v2.0.0-rc.1" {
		t.Fatalf("Latest(prerelease) = %q", withPrerelease.Tag)
	}
}

func TestClientLatestWithoutATokenSendsNone(t *testing.T) {
	t.Parallel()

	server, headers := releaseListServer(t, `[{"tag_name":"v1.0.0","prerelease":false,"draft":false}]`)

	client := &update.Client{API: server.URL, HTTP: server.Client()}

	if _, err := client.Latest(context.Background(), false); err != nil {
		t.Fatalf("Latest: %v", err)
	}

	if got := headers.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want nothing when no token is configured", got)
	}
}

func TestClientRefusesWhatItCannotUse(t *testing.T) {
	t.Parallel()

	t.Run("an empty list", func(t *testing.T) {
		t.Parallel()

		server, _ := releaseListServer(t, `[]`)

		client := &update.Client{API: server.URL, HTTP: server.Client()}

		if _, err := client.Latest(context.Background(), false); err == nil ||
			!strings.Contains(err.Error(), "No published release") {
			t.Fatalf("Latest = %v", err)
		}
	})

	t.Run("only prereleases, when stable is asked for", func(t *testing.T) {
		t.Parallel()

		server, _ := releaseListServer(t, `[{"tag_name":"v1.0.0-rc.1","prerelease":true}]`)

		client := &update.Client{API: server.URL, HTTP: server.Client()}

		if _, err := client.Latest(context.Background(), false); err == nil {
			t.Fatal("a prerelease was offered as the stable release")
		}
	})

	t.Run("a server that answers something else", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(http.StatusBadGateway)
		}))
		t.Cleanup(server.Close)

		client := &update.Client{API: server.URL, HTTP: server.Client()}

		if _, err := client.Latest(context.Background(), false); err == nil ||
			!strings.Contains(err.Error(), "502") {
			t.Fatalf("Latest = %v, want the status in the message", err)
		}
	})

	t.Run("a body that is not a release list", func(t *testing.T) {
		t.Parallel()

		server, _ := releaseListServer(t, `{"message":"Not Found"}`)

		client := &update.Client{API: server.URL, HTTP: server.Client()}

		if _, err := client.Latest(context.Background(), false); err == nil ||
			!strings.Contains(err.Error(), "not readable") {
			t.Fatalf("Latest = %v", err)
		}
	})
}

func TestClientByTag(t *testing.T) {
	t.Parallel()

	body := `[
	  {"tag_name":"v1.9.0","prerelease":false},
	  {"tag_name":"v2.0.0-rc.1","prerelease":true}
	]`

	server, _ := releaseListServer(t, body)

	client := &update.Client{API: server.URL, HTTP: server.Client()}

	for _, asked := range []string{"v1.9.0", "1.9.0"} {
		release, err := client.ByTag(context.Background(), asked)
		if err != nil || release.Tag != "v1.9.0" {
			t.Fatalf("ByTag(%q) = %+v, %v", asked, release, err)
		}
	}

	// A tag the user asked for by name is honoured even when it is a prerelease,
	// which is the whole point of naming it.
	if release, err := client.ByTag(context.Background(), "2.0.0-rc.1"); err != nil || release.Tag != "v2.0.0-rc.1" {
		t.Fatalf("ByTag(prerelease) = %+v, %v", release, err)
	}

	if _, err := client.ByTag(context.Background(), "v9.9.9"); err == nil ||
		!strings.Contains(err.Error(), "No release called") {
		t.Fatalf("ByTag(unknown) = %v", err)
	}
}
