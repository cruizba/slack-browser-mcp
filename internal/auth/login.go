package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/cruizba/slack-browser-mcp/internal/chrome"
	"github.com/cruizba/slack-browser-mcp/internal/failure"
	"github.com/cruizba/slack-browser-mcp/internal/platform"
)

// LoginURL is where the sign-in happens: the browser is only used to obtain the
// session, and every tool call after this goes straight to the Slack Web API.
const LoginURL = "https://app.slack.com/client"

const (
	// pollInterval is how often the page is asked whether the sign-in finished.
	pollInterval = 1500 * time.Millisecond
	// defaultCaptureTimeout is how long a person gets to sign in.
	defaultCaptureTimeout = 5 * time.Minute
)

// CapturedSession is what the browser had once the sign-in completed.
type CapturedSession struct {
	TeamID      string
	TeamName    string
	TeamDomain  string
	Token       string
	DCookie     string
	DCookieName string
}

// CaptureOptions is what a capture is asked for.
type CaptureOptions struct {
	Timeout    time.Duration
	Headless   bool
	ProfileDir string
	OnProgress func(string)
}

// CaptureSessions opens Chrome, waits for the sign-in and answers the sessions the
// browser is holding. A stale cookie for a secondary workspace is reported by the
// caller, not stored here: nothing reaches disk until Slack accepts it.
func CaptureSessions(options CaptureOptions) ([]CapturedSession, error) {
	timeout := options.Timeout
	if timeout == 0 {
		timeout = defaultCaptureTimeout
	}

	progress := options.OnProgress
	if progress == nil {
		progress = func(string) {}
	}

	dir := options.ProfileDir
	if dir == "" {
		return nil, errors.New("CaptureSessions needs a profile directory")
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating %s: %w", dir, err)
	}

	if err := platform.HardenDir(dir); err != nil {
		return nil, err
	}

	ctx := context.Background()

	browser, client, session, err := attach(ctx, dir, options.Headless)
	if err != nil {
		return nil, err
	}

	defer browser.Kill()
	defer func() { _ = client.Close() }()

	// A failed navigation is not fatal: the window is already open and the person can
	// finish there, which is exactly what a slow SSO redirect needs.
	_ = client.Navigate(ctx, session, LoginURL, 60*time.Second)

	progress("Sign in to Slack in the window that just opened.")

	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if sessions, ok := captured(ctx, client, session); ok {
			return sessions, nil
		}

		if browser.Exited() {
			return nil, &failure.AuthError{
				Message: "Chrome exited before the sign-in finished.",
				Hint:    "Another window may be using the same profile; close it and run `slack-browser login` again.",
			}
		}

		time.Sleep(pollInterval)
	}

	return nil, &failure.AuthError{
		Message: "No Slack session captured after " + strconv.Itoa(int(timeout.Seconds())) +
			"s. The window was closed, the sign-in did not complete, or Slack refused the browser." +
			" Screenshot: " + screenshot(ctx, client, session),
		Hint: "Run `slack-browser login` again and finish the sign-in in the browser.",
	}
}

// attach starts the browser and attaches to the page the sign-in will happen in.
func attach(ctx context.Context, dir string, headless bool) (*chrome.Chrome, *chrome.Client, string, error) {
	binary, err := chrome.FindChrome()
	if err != nil {
		return nil, nil, "", &failure.AuthError{
			Message: err.Error(),
			Hint:    "Install Chrome, or point CHROME_PATH at the binary.",
		}
	}

	browser, err := chrome.LaunchChrome(ctx, binary, dir, headless)
	if err != nil {
		return nil, nil, "", &failure.AuthError{
			Message: "Chrome did not start: " + err.Error(),
			Hint:    "Close any window using the profile and run `slack-browser login` again.",
		}
	}

	client, err := chrome.DialCDP(ctx, browser.WebSocketURL)
	if err != nil {
		browser.Kill()

		return nil, nil, "", &failure.AuthError{
			Message: "Chrome did not accept a connection: " + err.Error(),
			Hint:    "Run `slack-browser login` again.",
		}
	}

	target, err := client.FirstPage(ctx)
	if err != nil {
		_ = client.Close()

		browser.Kill()

		return nil, nil, "", &failure.AuthError{
			Message: "Chrome exposed no page to attach to: " + err.Error(),
			Hint:    "Run `slack-browser login` again.",
		}
	}

	session, err := client.Attach(ctx, target.TargetID)
	if err != nil {
		_ = client.Close()

		browser.Kill()

		return nil, nil, "", &failure.AuthError{
			Message: "Chrome refused the page attachment: " + err.Error(),
			Hint:    "Run `slack-browser login` again.",
		}
	}

	return browser, client, session, nil
}

// captured answers the sessions the page is holding, once it holds all of what a
// session needs: the cookie and at least one team with a token.
func captured(ctx context.Context, client *chrome.Client, session string) ([]CapturedSession, bool) {
	cookies, err := client.SlackCookies(ctx)
	if err != nil {
		return nil, false
	}

	cookie, found := chrome.PickSessionCookie(cookies)
	if !found {
		return nil, false
	}

	raw, err := client.ReadLocalConfig(ctx, session)
	if err != nil {
		return nil, false
	}

	teams := chrome.ParseBootTeams(raw)
	if len(teams) == 0 {
		return nil, false
	}

	sessions := make([]CapturedSession, 0, len(teams))

	for _, team := range teams {
		name := team.Name
		if name == "" {
			name = team.Domain
		}

		if name == "" {
			name = team.ID
		}

		sessions = append(sessions, CapturedSession{
			TeamID:      team.ID,
			TeamName:    name,
			TeamDomain:  team.Domain,
			Token:       team.Token,
			DCookie:     cookie.Value,
			DCookieName: cookie.Name,
		})
	}

	return sessions, true
}

// screenshot is best effort: it is a breadcrumb for a failure, not something worth
// failing a second time over.
func screenshot(ctx context.Context, client *chrome.Client, session string) string {
	path := filepath.Join(os.TempDir(), "slack-browser-login-failure.png")

	if err := client.Call(ctx, session, "Page.enable", struct{}{}, nil); err != nil {
		return path
	}

	var result struct {
		Data string `json:"data"`
	}

	if err := client.Call(ctx, session, "Page.captureScreenshot", map[string]any{"format": "png"}, &result); err != nil {
		return path
	}

	raw, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil {
		return path
	}

	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return path
	}

	return path
}
