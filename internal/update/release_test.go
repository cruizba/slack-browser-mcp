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
