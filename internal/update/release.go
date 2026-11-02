package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/failure"
)

// DefaultAPI is where the releases live. A test points it somewhere else, which is
// the reason it is a field and not a constant.
const DefaultAPI = "https://api.github.com/repos/cruizba/slack-browser-mcp/releases"

// Release is one published release, with what a download needs.
type Release struct {
	Tag        string  `json:"tag_name"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

// Asset is one file attached to a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// Client talks to the releases API.
type Client struct {
	// API is the releases endpoint, without a trailing slash.
	API string
	// Token authenticates the call when the repository is private or rate limited.
	Token string
	HTTP  *http.Client
}

// NewClient is the production client.
func NewClient(token string) *Client {
	return &Client{API: DefaultAPI, Token: token, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Latest answers the newest release, skipping prereleases unless asked for.
func (c *Client) Latest(ctx context.Context, prerelease bool) (Release, error) {
	releases, err := c.list(ctx)
	if err != nil {
		return Release{}, err
	}

	var best Release

	var bestVersion Version

	found := false

	for _, release := range releases {
		if release.Prerelease && !prerelease {
			continue
		}

		version, ok := ParseVersion(release.Tag)
		if !ok {
			continue
		}

		if !found || CompareVersions(version, bestVersion) > 0 {
			best = release
			bestVersion = version
			found = true
		}
	}

	if !found {
		return Release{}, &failure.StoreError{Message: "No published release to move to."}
	}

	return best, nil
}

// ByTag answers one release the user named, with or without its `v`.
func (c *Client) ByTag(ctx context.Context, tag string) (Release, error) {
	releases, err := c.list(ctx)
	if err != nil {
		return Release{}, err
	}

	wanted := strings.TrimSpace(tag)

	for _, release := range releases {
		if release.Tag == wanted || release.Tag == "v"+wanted {
			return release, nil
		}
	}

	return Release{}, &failure.StoreError{Message: "No release called " + wanted + "."}
}

func (c *Client) list(ctx context.Context) ([]Release, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.API, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Set("Accept", "application/vnd.github+json")

	if c.Token != "" {
		request.Header.Set("Authorization", "Bearer "+c.Token)
	}

	response, err := c.HTTP.Do(request)
	if err != nil {
		return nil, &failure.StoreError{Message: "Could not reach the release list: " + err.Error()}
	}

	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return nil, &failure.StoreError{Message: "The release list answered " + response.Status + "."}
	}

	var releases []Release
	if err := json.NewDecoder(response.Body).Decode(&releases); err != nil {
		return nil, &failure.StoreError{Message: "The release list is not readable: " + err.Error()}
	}

	return releases, nil
}

// AssetFor answers the file this build would replace itself with: the name goreleaser
// gives a target, so `slack-browser_0.8.0_linux_amd64`.
func AssetFor(release Release, goos, goarch string) (Asset, bool) {
	suffix := "_" + goos + "_" + goarch

	for _, asset := range release.Assets {
		name := strings.ToLower(asset.Name)

		if strings.Contains(name, suffix) && !strings.Contains(name, "checksums") &&
			!strings.Contains(name, "sha256") {
			return asset, true
		}
	}

	return Asset{}, false
}

// Checksums answers the published digests, keyed by file name.
func (c *Client) Checksums(ctx context.Context, release Release) (map[string]string, error) {
	for _, asset := range release.Assets {
		if !strings.Contains(strings.ToLower(asset.Name), "checksums") {
			continue
		}

		body, err := c.download(ctx, asset)
		if err != nil {
			return nil, err
		}

		return ParseChecksums(string(body)), nil
	}

	return nil, &failure.StoreError{Message: "Release " + release.Tag +
		" publishes no checksums, so a download cannot be verified."}
}

// ParseChecksums reads the `sha256  name` lines goreleaser writes.
func ParseChecksums(body string) map[string]string {
	digests := map[string]string{}

	for line := range strings.SplitSeq(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}

		digests[strings.TrimPrefix(fields[1], "*")] = strings.ToLower(fields[0])
	}

	return digests
}

func (c *Client) download(ctx context.Context, asset Asset) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return nil, err
	}

	response, err := c.HTTP.Do(request)
	if err != nil {
		return nil, &failure.StoreError{Message: "Could not download " + asset.Name + ": " + err.Error()}
	}

	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return nil, &failure.StoreError{Message: "Downloading " + asset.Name + " answered " + response.Status + "."}
	}

	return io.ReadAll(response.Body)
}

// Fetch answers the bytes of an asset, refusing anything whose digest does not match
// the published one: an update that installs an unverified binary is worse than no
// update at all.
func (c *Client) Fetch(ctx context.Context, release Release, asset Asset, digests map[string]string) ([]byte, error) {
	body, err := c.download(ctx, asset)
	if err != nil {
		return nil, err
	}

	wanted, known := digests[asset.Name]
	if !known {
		return nil, &failure.StoreError{Message: "The checksums for " + release.Tag +
			" do not mention " + asset.Name + ", so the download cannot be verified."}
	}

	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != wanted {
		return nil, &failure.StoreError{Message: "The download does not match its published checksum (" +
			got + " instead of " + wanted + "); nothing was replaced."}
	}

	return body, nil
}

// Replace puts a new binary where the running one lives.
//
// The new file is written beside the old one and renamed over it, which is what makes
// the swap atomic on Unix. Windows refuses to replace a file that is running, so there
// the old binary is moved aside first and the caller deletes it on the next run.
func Replace(target string, bytes []byte) error {
	dir := filepath.Dir(target)

	temporary, err := os.CreateTemp(dir, ".slack-browser-*")
	if err != nil {
		return &failure.StoreError{Message: "Cannot write next to " + target + ": " + err.Error()}
	}

	name := temporary.Name()

	if _, err := temporary.Write(bytes); err != nil {
		_ = temporary.Close()
		_ = os.Remove(name)

		return err
	}

	if err := temporary.Close(); err != nil {
		_ = os.Remove(name)

		return err
	}

	if err := os.Chmod(name, 0o755); err != nil { //nolint:gosec // an installed binary has to be executable
		_ = os.Remove(name)

		return err
	}

	if runtime.GOOS == "windows" {
		aside := target + ".old"
		_ = os.Remove(aside)

		if err := os.Rename(target, aside); err != nil {
			_ = os.Remove(name)

			return &failure.StoreError{Message: "Cannot set the running binary aside: " + err.Error()}
		}
	}

	if err := os.Rename(name, target); err != nil {
		_ = os.Remove(name)

		return &failure.StoreError{Message: "Cannot replace " + target + ": " + err.Error()}
	}

	return nil
}

// TargetName is the asset a build of this platform would download.
func TargetName(goos, goarch string) string {
	return fmt.Sprintf("slack-browser_%s_%s", goos, goarch)
}

// Describe says what an update would do, in the words the command prints.
func Describe(plan Plan) string {
	switch plan.Kind {
	case KindNoReleases:
		return "no releases published yet"
	case KindUpToDate:
		return "already on " + plan.Target + "; nothing to do"
	case KindAhead:
		if plan.Ahead == 0 {
			return "this build is newer than " + plan.Target + "; left alone"
		}

		return "this build is ahead of " + plan.Target + " by " + plural(plan.Ahead, "commit") + "; left alone"
	case KindDiverged:
		return "this build and " + plan.Target + " have diverged (" + plural(plan.Ahead, "commit") + " ahead, " +
			plural(plan.Behind, "commit") + " behind); nothing replaced"
	case KindUpdate:
		if plan.Behind == 0 {
			return "would move to " + plan.Target
		}

		return "would move to " + plan.Target + " (" + plural(plan.Behind, "commit") + " behind)"
	default:
		return "nothing to do"
	}
}

func plural(count int, word string) string {
	if count == 1 {
		return "1 " + word
	}

	return fmt.Sprintf("%d %ss", count, word)
}

// ErrNoUpdate is used when there is nothing to install.
var ErrNoUpdate = errors.New("nothing to update")
