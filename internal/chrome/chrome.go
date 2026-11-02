// Package chrome discovers, launches and signals a plain Chrome process, the way
// src/auth/chrome.ts does: this program starts the browser and only ever attaches to
// it over CDP, and no automation switch is passed, so the page does not report
// navigator.webdriver and Slack's sign-in accepts it.
package chrome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

// FindChrome answers the browser binary to launch: CHROME_PATH first, then the
// places a package installs it.
func FindChrome() (string, error) {
	return findChrome(platformCandidates(), os.Getenv("CHROME_PATH"))
}

// findChrome is the rule without the machine: the paths to try, in order, and what the
// user pointed at. It exists so the order and the error can be tested without a browser
// installed, which is what a test on a build runner has.
func findChrome(candidates []string, configured string) (string, error) {
	if configured != "" {
		candidates = append([]string{configured}, candidates...)
	}

	for _, candidate := range candidates {
		if candidate != "" && isExecutable(candidate) {
			return candidate, nil
		}
	}

	return "", errors.New("no Chrome/Chromium found; install Chrome or point CHROME_PATH at the binary")
}

// FreePort asks the kernel for a port and closes it before Chrome is told to
// use it: the gap is a race, as in the TypeScript version.
func FreePort() (int, error) {
	var config net.ListenConfig

	listener, err := config.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}

	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()

		return 0, errors.New("the kernel gave back an address that is not TCP")
	}

	port := address.Port

	if err := listener.Close(); err != nil {
		return 0, err
	}

	return port, nil
}

// Args mirrors chromeArgs() in src/auth/chrome.ts. --disable-sync and
// --disable-extensions are not cosmetic: an extension with the cookies
// permission can read `d`, which page JavaScript cannot.
func Args(profileDir string, port int, headless bool, extra ...string) []string {
	args := []string{
		"--user-data-dir=" + profileDir,
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-sync",
		"--disable-extensions",
		"--disable-features=Translate,MediaRouter",
	}

	args = append(args, extra...)
	if headless {
		args = append(args, "--headless=new")
	}

	return append(args, "about:blank")
}

type devtoolsVersion struct {
	Browser              string `json:"Browser"`
	ProtocolVersion      string `json:"Protocol-Version"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// Chrome is a running browser process we launched and therefore own.
type Chrome struct {
	Binary       string
	ProfileDir   string
	Port         int
	Endpoint     string
	WebSocketURL string

	cmd    *exec.Cmd
	exited chan struct{}
	stderr *tailBuffer
}

// LaunchChrome starts the browser with a dedicated profile and waits for its
// debugging endpoint, which is the only thing it is used for.
func LaunchChrome(ctx context.Context, binary, profileDir string, headless bool, extra ...string) (*Chrome, error) {
	port, err := FreePort()
	if err != nil {
		return nil, fmt.Errorf("free port: %w", err)
	}

	// The binary is the one this machine has, resolved by FindChrome or CHROME_PATH;
	// nothing here comes from a message or a remote caller.
	cmd := exec.CommandContext(ctx, binary, Args(profileDir, port, headless, extra...)...) //nolint:gosec // the binary is resolved locally, the arguments are ours
	cmd.Stdout = io.Discard
	stderr := &tailBuffer{max: 4096}
	cmd.Stderr = stderr
	configureProcess(cmd)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start chrome: %w", err)
	}

	exited := make(chan struct{})

	go func() {
		_ = cmd.Wait()

		close(exited)
	}()

	chrome := &Chrome{
		Binary:     binary,
		ProfileDir: profileDir,
		Port:       port,
		Endpoint:   fmt.Sprintf("http://127.0.0.1:%d", port),
		cmd:        cmd,
		exited:     exited,
		stderr:     stderr,
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return nil, fmt.Errorf(
				"chrome exited before it accepted connections; another window may be using the profile at %s: %s",
				profileDir, stderr.String())
		default:
		}

		if version, err := fetchDevtoolsVersion(chrome.Endpoint); err == nil && version.WebSocketDebuggerURL != "" {
			chrome.WebSocketURL = version.WebSocketDebuggerURL
			return chrome, nil
		}

		select {
		case <-ctx.Done():
			chrome.Kill()
			return nil, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}

	chrome.Kill()

	return nil, fmt.Errorf("chrome did not expose /json/version on port %d in 20s: %s", port, stderr.String())
}

// Exited reports whether the browser process is gone.
func (c *Chrome) Exited() bool {
	select {
	case <-c.exited:
		return true
	default:
		return false
	}
}

// Kill terminates the whole browser session: SIGTERM, then SIGKILL after 5s.
// Kill ends the browser and everything it started.
func (c *Chrome) Kill() {
	terminateProcess(c.cmd, c.exited)
}

func fetchDevtoolsVersion(endpoint string) (devtoolsVersion, error) {
	client := &http.Client{Timeout: 2 * time.Second}

	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint+"/json/version", nil)
	if err != nil {
		return devtoolsVersion{}, err
	}

	response, err := client.Do(request)
	if err != nil {
		return devtoolsVersion{}, err
	}

	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return devtoolsVersion{}, fmt.Errorf("GET /json/version: %s", response.Status)
	}

	var version devtoolsVersion
	if err := json.NewDecoder(response.Body).Decode(&version); err != nil {
		return devtoolsVersion{}, err
	}

	return version, nil
}

// tailBuffer keeps the last n bytes written to it, so a failed launch can say
// why Chrome exited.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}

	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	return string(t.buf)
}
