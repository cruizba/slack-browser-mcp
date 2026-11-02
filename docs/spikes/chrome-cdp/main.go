// Spike: port the interactive Chrome login of slack-browser-mcp to Go.
//
// It starts plain Chrome the way src/auth/chrome.ts does, attaches over CDP
// (no automation flags, no Playwright, no chromedp launcher), reads
// localConfig_v2/v1 from a page and the Slack session cookie from the store,
// and probes what a dropped connection does.
//
//	A sign-in needs a human, so nothing here calls app.slack.com.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"
)

const localPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>spike login page</title></head>
<body><p>no sign-in happens here</p>
<script>
const padding = "PADDING";
localStorage.setItem("localConfig_v2", JSON.stringify({
  padding,
  teams: {
    T0SPIKE02: {id: "T0SPIKE02", name: "Spike Side", domain: "spike-side", token: "xoxc-spike-v2-b"},
    T0SPIKE01: {id: "T0SPIKE01", name: "Spike HQ", domain: "spike-hq", token: "xoxc-spike-v2"}
  }
}));
localStorage.setItem("localConfig_v1", JSON.stringify({
  teams: {T0OLD0001: {id: "T0OLD0001", name: "Spike Legacy", domain: "spike-legacy", token: "xoxc-spike-v1"}}
}));
document.cookie = "d=xoxd-page-cookie; path=/";
window.__SPIKE_PAGE = "first-page";
</script></body></html>`

var failures []string

func pass(format string, args ...any) {
	fmt.Printf("  ok    "+format+"\n", args...)
}

func fail(name string, err error) {
	failures = append(failures, name+": "+err.Error())
	fmt.Printf("  FAIL  %s: %v\n", name, err)
}

func step(format string, args ...any) {
	fmt.Printf("\n== "+format+"\n", args...)
}

func main() {
	visible := flag.Bool("visible", false, "run the main browser with a window (the real login path)")
	keepProfile := flag.Bool("keep-profile", false, "keep the throwaway profile directory")
	flag.Parse()
	if err := run(*visible, *keepProfile); err != nil {
		fmt.Fprintf(os.Stderr, "\nFAILED: %v\n", err)
		os.Exit(1)
	}
	if len(failures) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d check(s) failed:\n  %s\n", len(failures), strings.Join(failures, "\n  "))
		os.Exit(1)
	}
	fmt.Println("\nall checks passed")
}

func run(visible, keepProfile bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	step("discovery")
	started := time.Now()
	binary, err := FindChrome()
	if err != nil {
		return err
	}
	pass("chrome binary %s", binary)

	profile, err := os.MkdirTemp("", "chrome-spike-profile-")
	if err != nil {
		return err
	}
	if err := os.Chmod(profile, 0o700); err != nil {
		return err
	}
	if !keepProfile {
		defer os.RemoveAll(profile)
	}

	step("launch")
	chrome, err := LaunchChrome(ctx, binary, profile, !visible)
	if err != nil {
		return err
	}
	defer chrome.Kill()
	pass("%s (headless=%t) ready in %s", binary, !visible, time.Since(started).Round(time.Millisecond))
	pass("endpoint %s", chrome.Endpoint)
	pass("browser websocket %s", chrome.WebSocketURL)

	version, err := fetchDevtoolsVersion(chrome.Endpoint)
	if err != nil {
		return err
	}
	pass("browser %q protocol %s", version.Browser, version.ProtocolVersion)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		page := strings.Replace(localPage, "PADDING", strings.Repeat("x", 100_000), 1)
		_, _ = writer.Write([]byte(page))
	}))
	defer server.Close()

	client, err := DialCDP(ctx, chrome.WebSocketURL)
	if err != nil {
		return err
	}
	pageTarget, err := client.FirstPage(ctx)
	if err != nil {
		return err
	}
	session, err := client.Attach(ctx, pageTarget.TargetID)
	if err != nil {
		return err
	}
	pass("attached page %s as session %s", pageTarget.TargetID, session)

	step("proof: read localConfig and the page cookie through CDP")
	if err := client.Navigate(ctx, session, server.URL, 15*time.Second); err != nil {
		return err
	}
	pass("navigated to %s", server.URL)

	raw, err := client.ReadLocalConfig(ctx, session)
	if err != nil {
		return err
	}
	for _, key := range StorageKeys {
		if raw[key] == nil {
			return fmt.Errorf("localStorage.%s missing after navigation", key)
		}
		pass("localStorage.%s read, %d bytes", key, len(*raw[key]))
	}
	if len(*raw["localConfig_v2"]) < 100_000 {
		fail("large localConfig", fmt.Errorf("expected a value over 100KB, got %d bytes", len(*raw["localConfig_v2"])))
	}

	teams := ParseBootTeams(raw)
	wantTeams := []string{"T0SPIKE01", "T0SPIKE02", "T0OLD0001"}
	if len(teams) != len(wantTeams) {
		return fmt.Errorf("parsed %d teams, want %d: %+v", len(teams), len(wantTeams), teams)
	}
	for index, want := range wantTeams {
		if teams[index].ID != want {
			return fmt.Errorf("team %d is %s, want %s (order: v2 before v1, sorted keys)", index, teams[index].ID, want)
		}
	}
	for _, team := range teams {
		pass("team %s %q domain=%s token=%s", team.ID, team.Name, team.Domain, team.Token)
	}

	pageCookies, err := client.Cookies(ctx)
	if err != nil {
		return err
	}
	foundPageCookie := false
	for _, cookie := range pageCookies {
		if !CookieHostMatches(cookie.Domain, "127.0.0.1") {
			continue
		}
		foundPageCookie = foundPageCookie || (cookie.Name == "d" && cookie.Value == "xoxd-page-cookie")
		pass("page cookie %s=%s domain=%s httpOnly=%t", cookie.Name, cookie.Value, cookie.Domain, cookie.HTTPOnly)
	}
	if !foundPageCookie {
		return fmt.Errorf("the cookie the page set is not visible in the browser cookie store")
	}

	step("landmine: coder/websocket caps a message at 32KiB by default")
	limited, err := DialCDPWithLimit(ctx, chrome.WebSocketURL, 32<<10)
	if err != nil {
		return err
	}
	limitedSession, err := limited.Attach(ctx, pageTarget.TargetID)
	if err != nil {
		return err
	}
	if _, err := limited.ReadLocalConfig(ctx, limitedSession); err == nil {
		fail("32KiB read limit", fmt.Errorf("a 100KB localConfig came back with the default limit"))
	} else {
		pass("with the 32KiB default the same read fails: %v", err)
	}
	_ = limited.Close()

	step("proof: the Slack session cookie, with the name it actually has")
	if err := client.SetCookie(ctx, Cookie{
		Name: "d-s", Value: "xoxd-spike-secure", Domain: ".slack.com", Path: "/",
		HTTPOnly: true, Secure: true, SameSite: "None",
	}); err != nil {
		return err
	}
	slackCookies, err := client.SlackCookies(ctx)
	if err != nil {
		return err
	}
	if len(slackCookies) != 1 {
		return fmt.Errorf("expected one slack.com cookie, got %+v", slackCookies)
	}
	pass("slack.com cookie %s=%s httpOnly=%t secure=%t", slackCookies[0].Name, slackCookies[0].Value, slackCookies[0].HTTPOnly, slackCookies[0].Secure)
	picked, ok := PickSessionCookie(slackCookies)
	if !ok || picked.Name != "d-s" {
		return fmt.Errorf("PickSessionCookie returned %q/%t, want d-s", picked.Name, ok)
	}
	pass("session cookie picked by name %q (d absent, httpOnly: page JS could not read it)", picked.Name)

	if err := client.DeleteCookie(ctx, session, "d-s", ".slack.com", "/"); err != nil {
		return err
	}
	if err := client.SetCookie(ctx, Cookie{
		Name: "d", Value: "xoxd-spike-plain", Domain: ".slack.com", Path: "/", HTTPOnly: true, Secure: true,
	}); err != nil {
		return err
	}
	slackCookies, err = client.SlackCookies(ctx)
	if err != nil {
		return err
	}
	picked, ok = PickSessionCookie(slackCookies)
	if !ok || picked.Name != "d" {
		return fmt.Errorf("PickSessionCookie returned %q/%t, want d", picked.Name, ok)
	}
	pass("session cookie picked by name %q after switching to d", picked.Name)
	if err := client.DeleteCookie(ctx, session, "d", ".slack.com", "/"); err != nil {
		return err
	}

	step("the anti-automation constraint, measured")
	webdriver, err := client.Evaluate(ctx, session, "navigator.webdriver")
	if err != nil {
		return err
	}
	pass("navigator.webdriver on our plain launch = %s", webdriver)
	if string(webdriver) != "false" {
		fail("navigator.webdriver", fmt.Errorf("plain launch reports %s, want false", webdriver))
	}
	if err := checkAutomationFlag(ctx, binary); err != nil {
		return err
	}

	step("concurrency: a second attach to the same browser and page")
	second, err := DialCDP(ctx, chrome.WebSocketURL)
	if err != nil {
		return err
	}
	defer second.Close()
	secondSession, err := second.Attach(ctx, pageTarget.TargetID)
	if err != nil {
		return err
	}
	marker, err := second.Evaluate(ctx, secondSession, "window.__SPIKE_PAGE")
	if err != nil {
		return err
	}
	if string(marker) != `"first-page"` {
		return fmt.Errorf("second client sees marker %s, so it is not the same page instance", marker)
	}
	pass("second client attached to the same page instance (marker %s)", marker)
	rawSecond, err := second.ReadLocalConfig(ctx, secondSession)
	if err != nil {
		return err
	}
	if len(ParseBootTeams(rawSecond)) != len(wantTeams) {
		return fmt.Errorf("second client parsed %d teams, want %d", len(ParseBootTeams(rawSecond)), len(wantTeams))
	}
	pass("both clients read the same localConfig concurrently")

	step("concurrency: detaching one client leaves the page and the other reads alive")
	if err := client.Close(); err != nil {
		pass("first client closed (%v)", err)
	} else {
		pass("first client closed cleanly")
	}
	if _, err := second.ReadLocalConfig(ctx, secondSession); err != nil {
		return fmt.Errorf("second client after the first detached: %w", err)
	}
	pass("second client still reads localConfig after the first detached")
	reattached, err := DialCDP(ctx, chrome.WebSocketURL)
	if err != nil {
		return err
	}
	defer reattached.Close()
	reattachSession, err := reattached.Attach(ctx, pageTarget.TargetID)
	if err != nil {
		return err
	}
	if _, err := reattached.ReadLocalConfig(ctx, reattachSession); err != nil {
		return fmt.Errorf("re-attach: %w", err)
	}
	pass("a fresh client re-attaches and reads without relaunching Chrome")
	if chrome.Exited() {
		return fmt.Errorf("chrome exited during the attach experiments")
	}

	step("concurrency: a read in flight when the CDP socket drops")
	dropClient, err := DialCDP(ctx, chrome.WebSocketURL)
	if err != nil {
		return err
	}
	dropSession, err := dropClient.Attach(ctx, pageTarget.TargetID)
	if err != nil {
		return err
	}
	results := readInFlight(ctx, dropClient, dropSession, 3000)
	time.Sleep(200 * time.Millisecond)
	abortedAt := time.Now()
	dropClient.Abort()
	select {
	case result := <-results:
		if result.err == nil {
			return fmt.Errorf("the in-flight read returned successfully after the socket was torn down")
		}
		pass("in-flight read failed %s after the socket closed: %v", time.Since(abortedAt).Round(time.Millisecond), result.err)
	case <-time.After(5 * time.Second):
		return fmt.Errorf("the in-flight read never returned after the socket was torn down")
	}
	recovered, err := DialCDP(ctx, chrome.WebSocketURL)
	if err != nil {
		return err
	}
	recoveredSession, err := recovered.Attach(ctx, pageTarget.TargetID)
	if err != nil {
		return err
	}
	if _, err := recovered.ReadLocalConfig(ctx, recoveredSession); err != nil {
		return fmt.Errorf("after a dropped socket: %w", err)
	}
	pass("after a dropped socket a new client still reads: the browser survived")
	_ = recovered.Close()

	step("concurrency: a read in flight when the browser dies")
	dieClient, err := DialCDP(ctx, chrome.WebSocketURL)
	if err != nil {
		return err
	}
	dieSession, err := dieClient.Attach(ctx, pageTarget.TargetID)
	if err != nil {
		return err
	}
	results = readInFlight(ctx, dieClient, dieSession, 5000)
	time.Sleep(200 * time.Millisecond)
	killedAt := time.Now()
	chrome.Kill()
	select {
	case result := <-results:
		if result.err == nil {
			return fmt.Errorf("the in-flight read succeeded while the browser was being killed")
		}
		pass("in-flight read failed %s after the browser was killed: %v", time.Since(killedAt).Round(time.Millisecond), result.err)
	case <-time.After(10 * time.Second):
		return fmt.Errorf("the in-flight read never returned after the browser was killed")
	}
	if !chrome.Exited() {
		fail("browser death", fmt.Errorf("chrome still running after Kill"))
	} else {
		pass("browser process is gone (Kill signalled the whole session)")
	}
	return nil
}

type callResult struct {
	err     error
	elapsed time.Duration
}

// readInFlight starts a read that takes seconds in the renderer, so the
// connection can be torn down while the call is outstanding.
func readInFlight(ctx context.Context, client *Client, sessionID string, busyMillis int) <-chan callResult {
	results := make(chan callResult, 1)
	expression := fmt.Sprintf(
		`(() => { const until = Date.now() + %d; while (Date.now() < until) {} return "done"; })()`,
		busyMillis)
	go func() {
		started := time.Now()
		err := client.Call(ctx, sessionID, "Runtime.evaluate", map[string]any{
			"expression":    expression,
			"returnByValue": true,
		}, nil)
		results <- callResult{err: err, elapsed: time.Since(started)}
	}()
	return results
}

// checkAutomationFlag proves the claim the whole design rests on: Chrome in
// --enable-automation mode reports navigator.webdriver, and that is what
// Playwright's own launcher passes.
func checkAutomationFlag(ctx context.Context, binary string) error {
	profile, err := os.MkdirTemp("", "chrome-spike-automation-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(profile)
	chrome, err := LaunchChrome(ctx, binary, profile, true, "--enable-automation")
	if err != nil {
		return err
	}
	defer chrome.Kill()
	client, err := DialCDP(ctx, chrome.WebSocketURL)
	if err != nil {
		return err
	}
	defer client.Close()
	page, err := client.FirstPage(ctx)
	if err != nil {
		return err
	}
	session, err := client.Attach(ctx, page.TargetID)
	if err != nil {
		return err
	}
	webdriver, err := client.Evaluate(ctx, session, "navigator.webdriver")
	if err != nil {
		return err
	}
	pass("navigator.webdriver with --enable-automation = %s", webdriver)
	if string(webdriver) != "true" {
		fail("navigator.webdriver with --enable-automation", fmt.Errorf("reports %s, want true", webdriver))
	}
	return nil
}
