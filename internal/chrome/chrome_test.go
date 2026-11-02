package chrome

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestFindChromeTakesCHROME_PATHFirst(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "my-chrome")

	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // the point is an executable candidate
		t.Fatalf("writing the candidate: %v", err)
	}

	t.Setenv("CHROME_PATH", binary)

	found, err := FindChrome()
	if err != nil {
		t.Fatalf("FindChrome: %v", err)
	}

	if found != binary {
		t.Fatalf("FindChrome = %q, want the one CHROME_PATH names", found)
	}
}

func TestFindChromeRefusesWhatCannotRun(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	plain := filepath.Join(dir, "not-executable")

	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}

	// A file that exists but cannot run is not a browser, and saying so beats starting
	// it and reading the error from exec. Windows has no execute bit: what decides there
	// is the extension, so this clause is a Unix one.
	if runtime.GOOS != "windows" && isExecutable(plain) {
		t.Fatal("a 0600 file was treated as executable")
	}

	// No candidate list at all, so the answer cannot depend on what this machine has
	// installed: the only path tried is the one that cannot run.
	if _, err := findChrome(nil, plain); err == nil {
		t.Fatal("a file that cannot run was chosen as the browser")
	}
}

func TestFindChromeSaysWhatToDoWhenNothingIsInstalled(t *testing.T) {
	t.Parallel()

	// The message is the whole error: it has to name the variable that fixes it, because
	// a user reading "no browser" with no next step is stuck.
	_, err := findChrome([]string{"/nonexistent/chromium", "/also/nonexistent"}, "/nonexistent/chrome")
	if err == nil {
		t.Fatal("discovery found a browser among paths that do not exist")
	}

	if !strings.Contains(err.Error(), "CHROME_PATH") {
		t.Fatalf("error = %q, want it to name CHROME_PATH", err)
	}
}

func TestFindChromePrefersWhatTheUserConfigured(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	first := filepath.Join(dir, "first")

	if err := os.WriteFile(first, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // the point is an executable candidate
		t.Fatalf("writing the candidate: %v", err)
	}

	if runtime.GOOS == "windows" {
		t.Skip("Windows decides by extension, not by an execute bit, so both files are candidates")
	}

	found, err := findChrome([]string{first}, "")
	if err != nil || found != first {
		t.Fatalf("findChrome = %q, %v", found, err)
	}

	// The configured path is tried before the discovered ones, and an empty configured
	// value means "nothing was asked for" rather than "the first candidate is empty".
	if found, err := findChrome([]string{}, ""); err == nil {
		t.Fatalf("findChrome with no candidates = %q", found)
	}
}

func TestArgsCarryTheProfileAndTheDebuggingPort(t *testing.T) {
	t.Setenv("PATH", os.Getenv("PATH"))

	args := strings.Join(Args("/tmp/profile", 9222, false), " ")

	for _, wanted := range []string{
		"--user-data-dir=/tmp/profile",
		"--remote-debugging-port=9222",
		"--no-first-run",
		// Not cosmetic: an extension with the cookies permission can read `d`, which
		// page JavaScript cannot.
		"--disable-sync",
		"--disable-extensions",
	} {
		if !strings.Contains(args, wanted) {
			t.Fatalf("args do not carry %q: %s", wanted, args)
		}
	}

	// The whole point of launching Chrome ourselves is that it does not report
	// navigator.webdriver, which is what the automation flag would do.
	if strings.Contains(args, "enable-automation") {
		t.Fatalf("the automation flag is back: %s", args)
	}

	if headless := strings.Join(Args("/tmp/profile", 9222, true), " "); !strings.Contains(headless, "--headless=new") {
		t.Fatalf("headless args: %s", headless)
	}
}

func TestFreePortAnswersAPortNobodyHolds(t *testing.T) {
	t.Parallel()

	port, err := FreePort()
	if err != nil {
		t.Fatalf("FreePort: %v", err)
	}

	var config net.ListenConfig

	listener, err := config.Listen(context.Background(), "tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("the port was not free: %v", err)
	}

	_ = listener.Close()
}

func TestFetchDevtoolsVersion(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")

		_, _ = response.Write([]byte(`{"Browser":"Chrome/156.0","Protocol-Version":"1.3","webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/browser/abc"}`))
	}))
	defer server.Close()

	version, err := fetchDevtoolsVersion(server.URL)
	if err != nil {
		t.Fatalf("fetchDevtoolsVersion: %v", err)
	}

	if !strings.Contains(version.Browser, "Chrome/156") {
		t.Fatalf("version = %+v", version)
	}

	if version.WebSocketDebuggerURL == "" {
		t.Fatal("the endpoint a client attaches to was not read")
	}
}

func TestFetchDevtoolsVersionRefusesAnError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	if _, err := fetchDevtoolsVersion(server.URL); err == nil {
		t.Fatal("a 503 was read as a version")
	}
}
