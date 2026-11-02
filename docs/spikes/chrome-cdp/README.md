# chrome-spike

Port of the interactive Chrome login of `slack-browser-mcp` to Go, without
Playwright: a plain Chrome process, launched by us, attached over CDP.

> **This is a spike, not shipped code.** It lives under its own `go.mod` so the
> root module never compiles it, and nothing imports it. Its job was to measure
> the port and to answer the questions that cannot be answered by reading
> `src/auth/login.ts`. Phase 2b starts from `chrome.go`, `cdp.go` and `page.go`
> and drops `main.go`, which is only the proof harness.

```
CGO_ENABLED=0 go run .            # headless, everything self-contained
CGO_ENABLED=0 go run . -visible   # the real login path: a real window
CGO_ENABLED=0 go test ./...       # hermetic: args, discovery, parsing, cookie choice
```

`main.go` is the proof harness: it drives the whole path against a local page,
28 checks, no Slack sign-in. The cost comparison against chromedp is recorded
below instead of kept as a second module.

## What the run proves

1. `FindChrome` finds the binary (`CHROME_PATH` first), `LaunchChrome` starts it
   with a dedicated profile and `--remote-debugging-port` and waits for
   `/json/version`.
2. A local `http` page writes `localConfig_v2`, `localConfig_v1` and a cookie;
   both storage keys (100 216 bytes and 112 bytes) and the cookie are read back
   through CDP, and the teams are parsed the way `login.ts` parses them.
3. The Slack session cookie reader is exercised on `.slack.com` cookies set with
   `httpOnly` + `secure`, once as `d-s` (with `d` absent) and once as `d`, and
   returns the name it found.
4. `navigator.webdriver` is `false` on our launch and `true` with
   `--enable-automation`, which is the flag Playwright's own launcher adds.

Measured on this machine (Chrome 156.0.8078.47, Linux): `/json/version` ready in
about 320 ms headless and 600 ms with a window (that includes process start).

## Concurrency answers

- **Second attach**: possible. Two websocket clients attached to the same page
  target at the same time both read `localConfig`; the page instance is shared
  (a marker set through the first client is visible to the second).
- **Detach**: closing one client leaves the page and the other client working.
  A fresh client re-attaches and reads without relaunching Chrome, so a login
  loop can recover from a dropped socket instead of failing.
- **Socket drop mid-read**: the in-flight `Runtime.evaluate` returns an error at
  once (`CDP connection closed: ...`), never a timeout, and the browser is
  unaffected.
- **Browser death mid-read**: same error, measured 36–40 ms after the kill, from
  the socket EOF. `Kill` takes the whole process group down.

## Things that will not port cleanly

- **`chromedp` cannot launch this browser at all**: v0.21.1 adds
  `--enable-automation` (`allocate.go:115`), which is the flag Slack refuses.
  Its attach path is also ambiguous to install: the module ships a `remote/`
  directory while `github.com/chromedp/chromedp/remote` exists separately at
  `v0.2.0`, so `go get` refuses with "found package … in multiple modules".
  `chromedp@v0.21.1` itself installs fine; the ambiguity only bites the moment
  the port adds the attach half it would need.
- **`coder/websocket` caps a message at 32 KiB** unless `SetReadLimit` raises it.
  The spike reads a 100 KB `localConfig`; with the default cap the same read
  fails with `message too big: read limited at 32769 bytes`.
- **`Storage.deleteCookies` does not exist** on Chrome 156 (`-32601`). Reading
  (`Storage.getCookies`) and writing (`Storage.setCookies`) are browser-level,
  deletion is `Network.deleteCookies` and needs a page session.
- **Browser-level vs page-level sessions are not interchangeable.**
  `Storage.getCookies` issued on a page session returns an empty list and no
  error. Silent wrong answer, not a crash.
- **`chromedp` v0.21 changed its API**: generics (`Action[T]`, `Run[T]`, `Do`),
  no `chromedp.ActionFunc`, cdproto commands are `cdp.Command` values called
  through `cdp.Call`. Code written against older examples does not compile.
- **Windows** has no process group; `proc_windows.go` kills the browser process
  only. A Job Object is the airtight version if the port needs one.
- **Anti-automation is not only about flags**: a CDP client can drive the page,
  and a page can detect that. The port keeps the TypeScript posture (observe,
  do not script the sign-in), which is why nothing here calls `Input` or
  `Runtime.callFunctionOn` against the sign-in form.

## Decisions taken before the port

- **Browsers: the Chromium family only.** Chrome, Chromium, Edge, Brave, Vivaldi
  and Opera, plus the Beta/Canary variants, with `CHROME_PATH` always first.
  Windows gets the same set under `%PROGRAMFILES%`, `%PROGRAMFILES(X86)%` and
  `%LOCALAPPDATA%`, so a machine with Edge and no Chrome still logs in.
- **Firefox is out, on purpose.** It dropped CDP in Firefox 141, so it speaks
  WebDriver BiDi and would need a second protocol client just for the login.
  When no browser is found the error names what to install and says why Firefox
  does not work, rather than failing with "Chrome not found".
- **Follow a new tab.** `Target.setDiscoverTargets` instead of polling
  `context.pages()[0]`. This is a deliberate divergence from the TypeScript,
  which inherits the bug; it is a fix, not a port difference.
- **Token fallback comes after parity.** `Network.requestWillBeSent` plus
  `getRequestPostData` recovers the token from the sign-in request itself, but
  it is the one mechanism this spike did not exercise, so it waits until the
  port can be checked against a real login.
