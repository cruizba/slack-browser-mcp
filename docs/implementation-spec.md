# Implementation spec

Design of record for `slack-browser-mcp`: why it is shaped this way, what the
contracts are, and what comes next.

## 1. Goal

Let any agent running on this machine read — and eventually write — messages in
Slack conversations the user belongs to, including private channels and group
DMs, without asking a workspace admin to install an app.

"Any agent" means one capability, several clients: pi, Claude Code, opencode and
plain shell scripts. MCP is the transport; a CLI over the same core covers
everything that has bash but no MCP.

## 2. Non-goals

- A Slack app, bot user or OAuth install flow. That path needs workspace admin
  approval and only sees channels the bot is invited to.
- DOM automation of the Slack web client. See §3.
- Multi-user or hosted operation. The store is a single user's session.
- Replacing the Slack client. This is a read surface for agents, not a UI.

## 3. Why browser for login and API for everything else

Slack's web client authenticates with a pair that is not available to public
apps:

- `xoxc-…`: the workspace token, tied to a logged-in web session.
- `d=xoxd-…`: a session cookie, required alongside the token.

Scraping the rendered page instead would mean fighting a virtualised message
list and hashed class names on every Slack deploy. Using the pair against
`https://slack.com/api` returns the same structured payloads the real client
uses, and survives UI changes.

Consequence: Playwright is a **login-time** dependency only. No browser runs
during normal tool calls, which keeps latency, memory and failure modes small.

## 4. Architecture

```
CLI (src/cli.ts) ─┐
                  ├─→ auth/store.ts ──→ ~/.config/slack-browser-mcp/accounts/*.json (0600)
MCP (src/mcp) ────┘          │              config/settings.ts ──→ …/config.json (default workspace)
                             ↓
                    slack/client.ts ──→ https://slack.com/api (xoxc + d cookie)
                             ↑
auth/login.ts (Playwright, login only)

archive/sync.ts ──→ archive/store.ts ──→ archive/<TEAM>/<CHANNEL>.jsonl
        │                   └─────────→ archive/state.json
        └─────────→ archive/index.ts ──→ archive.sqlite (FTS5, rebuildable)
```

Rules that hold across layers:

- `slack/client.ts` never reads the filesystem and never launches a browser.
- `auth/store.ts` never performs network calls.
- `src/mcp` and `src/cli.ts` contain no protocol details beyond their own
  surface; both build a `SlackClient` from a stored account and delegate.

## 5. Authentication

### 5.1 What is captured

| Value | Shape | Lifetime |
| --- | --- | --- |
| token | `xoxc-…` | Until logout, password change, or 2FA reset on that workspace |
| cookie | `xoxd-…`, URL-encoded, sent as `Cookie: d=<value>` | Same event as the token |

There is no refresh token and no way to extend either half. `slack-browser login`
is the only recovery path, so every auth failure must say exactly that.

Cookie names: Slack now sets both `d` and `d-s` (the secure/httpOnly variant).
The capture stores whichever exists, plus its name, and the client sends it back
under that name. Hard-coding `d` breaks accounts where only `d-s` is present.

### 5.2 Capture order

1. Start Chrome directly (`src/auth/chrome.ts`) with `--user-data-dir=<profile>`
   and `--remote-debugging-port=<free port>`, then attach with
   `chromium.connectOverCDP`. Playwright's own launcher must **not** be used for
   the login: it passes `--enable-automation`, which sets `navigator.webdriver`,
   and Slack answers with "Couldn't sign you in — this browser or app may not be
   secure". Attaching over CDP leaves the page as an ordinary browsing session.
   The profile is dedicated: the user's own Chrome profile must never be reused,
   and Chrome's cookie store is encrypted per profile.
2. Point the page at `https://app.slack.com/client`.
3. Poll every 1.5 s until the deadline:
   - **Primary:** read `localStorage.localConfig_v2` (then `localConfig_v1`) and
     parse `teams[].{id,name,domain,token}`.
   - **Fallback:** capture `token=` fields from POSTs to `/api/` fired by the
     page itself, and attribute them to the team id in the URL (`/client/T…`).
     This survives the storage key changing shape, which has happened before.
4. Collect the `d`/`d-s` cookie from the context (httpOnly included).

### 5.3 Validation before persistence

Each captured session is probed with `auth.test` before it is written. This
matters for multi-workspace accounts: the `d` cookie is per workspace, so a
session captured for workspace B may hold workspace A's cookie. Rejected
sessions are reported with their `auth.test` error and are not stored — a stale
entry in the store would surface later as a confusing `invalid_auth`.

### 5.4 Lifecycle

- On timeout the capture writes a screenshot to
  `/tmp/slack-browser-login-failure.png` and reports the page title, so a refused
  sign-in can be diagnosed without reproducing it.
- One login per workspace; the persistent profile means the second login usually
  needs no credentials.
- `auth_status` and `slack-browser status` refresh `last_ok_at`.
- `REAUTH_ERRORS` (`invalid_auth`, `not_authed`, `token_revoked`,
  `token_expired`, `account_inactive`) raise `AuthError`, which carries the
  re-login hint. No silent retry: retrying a revoked session just burns calls.
- `missing_scope` / `not_allowed_token_type` are **not** re-auth errors. They are
  reported as `SlackApiError` because a fresh login will not fix them.

## 6. Credential store contract

```ts
interface CredentialStore {
  list(): Promise<AccountSummary[]>          // no secrets
  get(teamId: string): Promise<SlackAccount>
  resolve(reference?: string): Promise<SlackAccount>
  default(): Promise<DefaultWorkspace | null>
  save(account: SlackAccount): Promise<void>
  markValidated(teamId: string, when?: Date): Promise<void>
  remove(teamId: string): Promise<void>
}
```

File format (`<team_id>.json`):

```json
{
  "team_id": "T01ACME",
  "team_name": "Acme",
  "team_domain": "acme",
  "user": "ana",
  "user_id": "U01ANA",
  "token": "xoxc-…",
  "d_cookie": "xoxd-…%2B…",
  "d_cookie_name": "d",
  "saved_at": "2026-10-20T15:00:00.000Z",
  "last_ok_at": "2026-10-20T15:02:11.000Z"
}
```

Invariants:

- File `0600`, directory `0700`, owner is the current uid. A wider mode is a
  hard error with the exact `chmod` to run, not a warning: a world-readable token
  is already compromised.
- Writes are atomic (temp file at `0600` in the same directory, then `rename`),
  so a crash cannot leave a half-written account.
- `team_id` is validated against `^[A-Za-z0-9_-]{1,64}$` before it becomes a file
  name.
- `AccountSummary` is the only shape that may be shown to an agent; the token and
  cookie are not part of it, so no tool can leak them by accident.
- Rotation is implemented behind this interface: swapping in an `age` or
  libsecret-backed store must not touch callers.

`resolve` precedence: an explicit reference (exact `team_id`, then exact
domain, then exact name, then substring of name/domain; ambiguity is an error
listing candidates rather than a guess), then `SLACK_BROWSER_MCP_TEAM`, then
`default_workspace` from the preferences file, then the only stored account. With
several accounts and no default it fails and lists them, naming the command that
sets a default. `default()` reports the same choice plus its source, so the CLI
and `auth_status` can show it without guessing.

### 6.1 Preferences

`~/.config/slack-browser-mcp/config.json`, `0600`, plain JSON so it can be edited
by hand:

```json
{
  "default_workspace": "T01ACME"
}
```

The value is a team id, not the name the user typed: names and domains change,
ids do not. `slack-browser workspace use <ref>` resolves the reference first, so
an ambiguous or unknown name fails at `use` time instead of on every later call.
The file is written atomically and validated on read; an unknown key or broken
JSON is an error naming the file, not a silent fallback, because a default that
is quietly ignored looks like the tool reading the wrong workspace.

`SLACK_BROWSER_MCP_TEAM` stays as a one-off override for a single shell or an MCP
client's `env`, and wins over the file so a temporary choice never needs editing.

## 7. Slack API layer

`POST https://slack.com/api/<method>`, `application/x-www-form-urlencoded`, with
`token` in the form body and the cookie header. Form-encoded matches what the web
client sends and avoids an `Authorization` path that behaves differently for
`xoxc` tokens.

Endpoints in use: `auth.test`, `users.conversations`, `conversations.history`,
`conversations.replies`, `users.info`, `users.list`, `conversations.list`,
`usergroups.list`, `files.info`, plus an authenticated `GET` of `url_private` for
attachment bytes.

Behaviours:

- `429` / `503` and `ratelimited` / `internal_error` / `fatal_error` /
  `service_unavailable` / `request_timeout` retry up to 3 times, honouring
  `Retry-After` and falling back to exponential backoff (400 ms, capped 4 s).
- `users.conversations` is paginated to 20 pages of 200, filtered to
  `public_channel,private_channel,mpim,im`, and cached per client instance.
- `users.info` results are cached per user id; failures degrade to the raw id
  instead of failing a read.
- `users.list`, `conversations.list` and `usergroups.list` feed a directory that
  resolves mentions, channel references and user groups. It is stored under the
  team's archive directory and refreshed once a day; the optional sources may
  fail without losing the rest.
- Attachment bytes are fetched with the same `xoxc` token plus `d` cookie sent as
  `Authorization: Bearer …` (Slack redirects only between its own hosts), and are
  stored under `archive/<team>/files/` when `read_file` asks for them.
- Message text is truncated at 2000 chars per message; the rest of the payload
  stays out of the model's context by design.

## 8. Agent surface

### MCP tools

| Tool | Input | Output | Annotations |
| --- | --- | --- | --- |
| `auth_status` | `workspace?` | one line per account: `ok`/`FAIL` + team + user | read-only |
| `list_conversations` | `workspace?`, `query?`, `kind?=all`, `limit?=100` | `label (id) [N members]`, sorted by name | read-only |
| `read_messages` | `workspace?`, `channel`, `limit?=20`, `oldest?`, `latest?` | header + oldest-first lines + paging footer | read-only |
| `get_thread` | `workspace?`, `channel`, `thread_ts`, `limit?=100` | header + thread lines | read-only |
| `post_message` | `workspace?`, `channel`, `text`, `thread_ts?`, `confirm=false` | `Sent to <where> at ts <ts>` + audit path, or the exact dry-run text | destructive |
| `read_file` | `workspace?`, `file`, `refresh=false` | saved path + summary, plus an `image` block for images up to 4 MB | read-only |
| `search_messages` | `query`, `workspace?`, `channel?`, `limit?=20`, `include_deleted=false` | `archive: <age>` then ranked hits `[ts] channel author: text`, prefixed with the workspace when the search spans more than one | read-only |
| `archive_status` | `workspace?` | last sync, channels, thread backlog, errors, lock holder | read-only |

Every tool's `workspace` argument accepts a name, domain or team id and falls
back to the default described in §6; `auth_status` prints which one that is and
why. Without an argument, hits and archive status lines carry the workspace name
so a result set that spans workspaces is never ambiguous.

`channel` also accepts `me`, which opens the user's own DM through
`conversations.open` and posts there.

`post_message` is the only destructive tool. Mechanics of the gate:

- `confirm` defaults to `false`. A call without it resolves the channel (to name
  the real target), returns the exact text, and posts nothing. Resolving is a
  read, so a typo in the channel name surfaces before the send, not after.
- With `confirm=true` it calls `chat.postMessage` once, with `thread_ts` when
  given so a reply cannot land in the channel timeline by mistake.
- Every successful send appends one line to
  `$XDG_DATA_HOME/slack-browser-mcp/audit.jsonl` (`0600`): timestamp, workspace,
  channel label and id, message ts, character count, and a redacted 120-char
  preview. The full text is never stored, so the audit cannot become a second
  copy of secrets someone pasted into a channel.

Every message line is `[<ts>] <iso time> <author>: <text>`, plus
`(N replies, thread_ts <ts>)` when a parent has replies, plus one
`[file: <name> id=<file id>]` per attachment. The raw `ts` is the handle for
follow-up calls (`thread_ts`, `oldest`, `latest`); the file id is the handle for
`read_file`.

Names come from the directory described in §7: `<@U…>`, `<#C…>`, `<!subteam^S…>`,
`<!here>` and links are resolved at render time, so the model never reads a raw id
it cannot act on. The archive keeps the markup and resolution stays a display
concern.

Limits exist to protect the context window: `read_messages` caps at 200, and the
footer tells the model how to page rather than dumping history.

### CLI

`login`, `accounts`, `workspace [list|use|clear]`, `status`, `read`, `thread`,
`send`, `sync`, `archive search|stats|reindex|migrate`, `serve`. Same core, no duplicated
logic. Diagnostics go to stderr; in `serve` mode stdout belongs to the MCP
protocol. `sync` is the only command that ignores the default on purpose: without
`--workspace` it walks every stored workspace under one shared deadline, so one
timer keeps the whole archive current and a slow workspace cannot push the rest
past the budget.

## 9. Archive

A plain-text mirror of the conversations, so history survives and search does not
cost a request.

### 9.1 Shape

```
$XDG_DATA_HOME/slack-browser-mcp/archive/
├── schema.json                    # layout version, written by the migration step
├── state.json                     # per-channel cursors, backlogs, last errors
└── <TEAM_ID>/
    ├── <CHANNEL_ID>.jsonl         # one JSON object per line
    ├── directory.json             # users, channels and groups: ids to names
    ├── files/                     # attachments fetched by read_file
    └── ...
$XDG_DATA_HOME/slack-browser-mcp/archive.sqlite   # rebuildable FTS5 index
```

JSONL is the source of truth and the index is derived data: `archive reindex`
drops it and rebuilds from the files, so a corrupt index costs seconds, never
history.

A line is a record, not a message. Slack messages are immutable apart from edits
and deletions, so the file is an append-only event log where **the last line for a
`ts` wins**:

```json
{"ts":"1700000000.000100","channel":"C01CI","channel_name":"ci-notifications","user":"U01…","text":"deploy finished","reply_count":0,"synced_at":"2026-10-20T13:56:28.251Z"}
{"ts":"1700000100.000200","channel":"C01CI","channel_name":"ci-notifications","user":"U02…","text":"hello from the thread","thread_ts":null,"synced_at":"2026-10-20T13:59:00.000Z"}
{"ts":"1700000100.000200","channel":"C01CI","deleted_at":"2026-10-20T14:02:11.000Z","synced_at":"2026-10-20T14:02:11.000Z"}
```

A deletion is a line with `deleted_at`, and the original text stays in the file.
Erasing it would make the archive claim the message never existed — exactly the
lie a personal archive exists to avoid. `state.json` carries the cursor
(`newest_ts`), the last reconcile, the pending-thread backlog and the last error
per channel.

Threads are messages with `thread_ts`, in the same file as their channel: a
thread has no independent existence in Slack, and splitting it into its own store
would only add joins.

### 9.2 Sync algorithm

Per channel, in least-recently-synced order:

1. **Incremental.** `conversations.history` with `oldest=<newest_ts>` and cursors,
   capped at 20 pages of 200. First run (or `--full`) uses `--since <days>`
   instead, 30 by default.
2. **Reconcile, once per 24 h.** Re-read a 7-day window and compare: messages in
   the archive that are no longer returned are marked deleted. This is the only
   way to see deletions, because Slack has no delta feed without an app.
3. **Threads.** A parent with `reply_count > 0` whose stored count differs is
   queued; up to `maxThreadsPerChannel` (10) parents are fetched per run with
   `conversations.replies`. The rest wait in `pending_threads`, persisted, so a
   first backfill drains over several runs instead of one burst.

Reply records are excluded from reconcile deletion detection: they never appear in
channel history, so their absence there means nothing.

The run has a wall-clock budget (`--deadline`, 10 minutes). When it expires the
remaining channels keep their old state and are picked up next time, because the
cursor is saved after every channel. Least-recently-synced ordering means a
wedged channel cannot starve the others. With several workspaces the budget is
shared across them in turn: each `syncAccount` gets whatever is left, and the
ones that did not run are reported instead of silently skipped. A workspace whose
session died fails alone; the others still sync.

### 9.3 Concurrency and freshness

Two syncs at once would lose cursors: `state.json` is read-modify-write, the JSONL
appends interleave and SQLite would answer `SQLITE_BUSY`. Every entry point
(timer, CLI, catch-up) therefore takes a lock directory, `archive/.sync.lock`,
before touching anything. Node has no `flock` binding and re-execing the whole run
under `flock(1)` only to hold a lock is worse, so `mkdir` — atomic on every
filesystem we care about — plus an owner file with pid, host and start time. A
holder whose pid is gone, or whose timestamp is older than six hours, is taken
over; a live holder is never preempted. The lock is released in a `finally`.

The server does **not** run the sync loop, and the reason is structural rather
than stylistic:

- A stdio MCP server lives as long as the agent session. Pi spawns every enabled
  server at session start and kills it at the end; Claude Code does the same. "The
  server syncs" would mean "the archive syncs only when an agent is open", and days
  without sessions would slide the 7-day reconcile window past deletions that can
  never be recovered. A timer with `Persistent=true` is the only structure that
  guarantees coverage.
- Requests have a 60-second budget in pi. An incremental run takes ~19 s today but
  minutes after an idle week; a full backfill took 63 s. Doing that inside a tool
  call either blows the budget or makes a search feel hung.
- N agent sessions would mean N servers racing for the same files.

What the server does instead is be honest about time and nudge the work out of
band: reads report the archive's age, and when it is older than 15 minutes the
server spawns a detached `sync --quiet --deadline 60` after checking that the lock
is free, then answers from the current index. The nudge writes to local disk only,
never to Slack, so it does not violate R6. `SLACK_BROWSER_MCP_NO_AUTOSYNC=1`
disables it for machines where background work is unwelcome.

### 9.4 Known limits

- Edits and deletions are only visible inside the reconcile window. Anything that
  happened before the first sync, or older than 7 days, can be stale forever.
- Thread updates on parents older than the newest channel message are noticed by
  the daily reconcile, so they can lag up to 24 hours.
- Deleted messages stay in the archive with their text. That is deliberate, and it
  is also why the directory is `0700`.
- Reactions and pins are not archived. Attachment bytes are, but only when
  `read_file` asks for one: the JSONL keeps the id, name, mimetype and size, not
  the content.

### 9.5 Schema and migrations

The archive outlives a single build: a systemd timer, an MCP server and a shell
can be running different versions against the same files. So the layout carries a
version in `archive/schema.json` (`{"version": 2, …}`), and data written before
that marker existed counts as version 1, because the layout has not changed since.

`ensureArchiveSchema` (`src/archive/schema.ts`) runs before `sync`, before
`archive search|stats|reindex`, and before the MCP tools that read the archive:

- A version newer than the build is refused, naming the update, instead of being
  read with the wrong assumptions.
- It takes the same lock as `sync`, so a migration never rewrites a file while an
  append is writing it, and a read reports `locked` instead of waiting.
- Every migration above the stored version runs in order; the marker is written
  once they all succeed.
- It drops and rebuilds the FTS index from the migrated JSONL inside the same
  step, because `sync` only adds new rows: leaving it empty would make search
  blind until someone remembered `archive reindex`.
- A channel file is only rewritten when a line actually changes, so a second run
  touches nothing.

Migrations are idempotent, so a crash halfway converges on the next run. A line
that cannot be parsed moves to `<channel>.jsonl.corrupt` instead of blocking the
upgrade or vanishing.

| Version | Change |
| --- | --- |
| 1 | implicit: JSONL per channel, `state.json`, no marker |
| 2 | attachment records carry `id`/`mimetype`/`size`; null-valued fields are omitted |

Readers keep tolerating the previous shape (`files: ["name"]` still folds into
`{name}`), because not every path migrates: `slack-browser read` is live-only, and
an already-running MCP server can outlive an upgrade. `directory.json` is a cache
with its own `version`; on a mismatch it is simply fetched again.

`slack-browser archive migrate` runs the same step by hand and prints the version.

## 10. Error model

| Situation | Type | Agent sees |
| --- | --- | --- |
| Session rejected by Slack | `AuthError` | message + "run `slack-browser login`" |
| Slack refused the operation | `SlackApiError(method, code)` | `method failed: code` |
| Channel name unknown / ambiguous | `Error` from `resolveChannel` | candidates to choose from |
| Store missing / wrong permissions | `StoreError` | path + the `chmod` to run |
| Archive written by a newer build | `SchemaError` | version found + "update slack-browser" |
| Rate limited after retries | `SlackApiError(http_429)` | wait and retry |

All agent-facing error text passes through `redact` before leaving the process.

## 11. Security requirements

- R1 — Secrets never cross the tool boundary. No tool returns a token, cookie or
  cookie name.
- R2 — The store enforces `0600` + ownership, and fails closed.
- R3 — Every outbound string is redacted for `xox?-…`, `token=…` and `Bearer …`.
- R4 — The browser profile is dedicated and lives under `$XDG_DATA_HOME`; the
  user's own Chrome profile is never touched.
- R5 — Credentials are not written to the repository, logs or shell history.
- R5b — The archive is treated as credentials-grade data: it is a plaintext copy
  of private conversations. `0700` directories, `0600` files, no deletion of the
  original text on a tombstone, and it is documented as landing in full-disk
  backups.
- R6 — Least capability: read-only tools are annotated `readOnlyHint`, and the
  only write tool requires `confirm=true` on every call. A read never sends.

## 12. Testing

Unit tests need no browser and no network:

- `test/store.test.ts` — mode enforcement, atomic save, resolve precedence and
  ambiguity, secret-free summaries, removal.
- `test/settings.test.ts` — preferences file mode and atomic write, clearing the
  default, broken JSON and unknown keys, and the full precedence chain: explicit
  reference, `SLACK_BROWSER_MCP_TEAM`, `config.json`, only stored session.
- `test/client.test.ts` — request shape (token in form, cookie header), response
  parsing, `invalid_auth` → `AuthError`, error codes, `429` + `Retry-After`
  retries, channel resolution by name and by id.
- `test/redact.test.ts` — token shapes, form/query parameters, bearer headers.
- `test/chrome.test.ts` — Chrome binary discovery (`CHROME_PATH`, executable
  check) and debug-port allocation.
- `test/mcp.test.ts` — in-memory MCP transport: tool list, descriptions,
  read-only annotations, and a graceful answer with an empty store.
- `test/references.test.ts` — mention, channel, user-group, special-mention, link
  and entity resolution, including the raw-id fallback and the rendered file id.
- `test/directory.test.ts` — directory fetch with a failing optional source,
  0600/atomic storage, corrupt file tolerated, freshness window, and the stale
  copy serving when a refresh fails.
- `test/files.test.ts` — safe file names that cannot escape the team directory,
  idempotent overwrite, find by id, and permissions.
- `test/mcp-write.test.ts` — the confirm gate end to end over the MCP transport:
  dry run posts nothing, `confirm=true` posts exactly once with the right
  channel, text and token, and the send lands in the audit log.
- `test/audit.test.ts` — one line per send, `0600`, bounded and redacted preview.
- `test/archive.test.ts` — JSONL folding, tombstones that keep the text, a message
  that reappears clearing `deleted_at`, file records from before ids existed,
  permissions, FTS search with user-typed operators, deleted filtering, and a
  rebuild from plain text after a reset.
- `test/schema.test.ts` — fresh marker, legacy records upgraded in place with
  corrupt lines quarantined, a second run touching nothing, the derived index
  dropped, a newer version refused, and the sync lock deferring the migration.
- `test/sync.test.ts` — first backfill plus threads, incremental cursor on the
  second run, no refetch when `reply_count` is unchanged, daily reconcile marking
  disappeared messages, thread backlog draining across runs, and no rewritten
  lines when nothing changed.

Verified against a real workspace on 2026-10-20: login over Google SSO with 2FA,
read of one channel, and two `chat.postMessage` sends (channel and self-DM), each
read back afterwards.

Manual checklist for the login path (cannot be automated without a real account):

1. `slack-browser login` on a workspace with SSO; confirm a session is stored and
   `status` reports `ok`.
2. Restart with a `chmod 644` store; confirm the refusal message.
3. Log out of Slack in a browser; confirm `read` fails with the re-login hint.
4. Log in to two workspaces; confirm both are stored and `--workspace` selects
   between them.

## 13. Milestones

**M0 — read path (done)**

- [x] Playwright capture with storage + network fallbacks
- [x] `auth.test` validation before persistence
- [x] `0600` store behind a `CredentialStore` interface
- [x] `SlackClient` with retries, rate limiting, error taxonomy
- [x] MCP tools `auth_status`, `list_conversations`, `read_messages`, `get_thread`
- [x] CLI `login`, `accounts`, `status`, `read`, `thread`, `serve`
- [x] Unit + in-memory MCP tests

**M1 — write path (done)**

- [x] `post_message` over `chat.postMessage`, including thread replies
- [x] Dry-run by default in the CLI (`--send` to commit); MCP tool annotated
      `destructiveHint` with a required `confirm: true` argument
- [x] Append-only audit log at
      `$XDG_DATA_HOME/slack-browser-mcp/audit.jsonl` (ts, workspace, channel,
      message ts, redacted 120-char preview)
- [x] Reply in thread when `thread_ts` is given, instead of the channel timeline
- [x] Tests: request body shape, refusal without confirmation, single post with
      confirmation, audit entry

**Remaining in M1**

- [ ] `add_reaction`
- [ ] Surface the audit path in `auth_status`

**M2 — archive (done)**

- [x] JSONL archive per channel with tombstones and a rebuildable FTS5 index
- [x] Incremental sync with a daily reconcile window and a persisted thread backlog
- [x] CLI `sync`, `archive search|stats|reindex`; MCP `search_messages`
- [x] systemd user timer every 15 minutes with `Persistent=true`
- [x] Tests without network, plus a real run over 91 conversations
- [x] Single-writer lock, freshness reporting, and a detached catch-up nudge

**M3 — polish**

- [x] Resolve mentions, channel references, user groups and links through a
      cached directory instead of leaking raw ids
- [x] `read_file`: fetch attachment bytes on demand and return images as an
      image block
- [x] Versioned archive layout with in-place migrations, a sync-lock guard and a
      derived index that is rebuilt instead of trusted
- [ ] Cursor pass-through for `read_messages` instead of only `oldest`
- [ ] Serve archived history to `read_messages` when the requested range is cached
- [ ] Retention policy (`slack-browser archive prune --older-than`)
- [ ] `slack-browser logout <workspace>` and profile reset
- [ ] Optional keyring/`age` credential store behind the existing interface
- [ ] Skill with the operational habits: which channels matter, how to summarise
      a thread, when not to post

## 14. Risks and open questions

- **Private API.** `xoxc` + `d` is undocumented; Slack can rotate or restrict it.
  Mitigation: capture and API layers are separate, so a replacement transport
  (real app token, or browser-based DOM if it ever comes to that) stays local to
  `src/slack/client.ts`.
- **Per-workspace cookies.** Multi-workspace capture may pair a token with the
  wrong cookie; §5.3 validation contains the damage, but the UX for "log in again
  to that other workspace" is unpolished until M2.
- **ToS.** Automating a user session is a grey area. Acceptable for the user's
  own account on their own workspaces; this tool should not be distributed as a
  service.
- **Prompt-injection surface.** Channel content is untrusted input. A message
  that says "reply with the tokens" must not be able to trigger a send: the
  `confirm` gate makes every send an explicit decision, R1 keeps credentials out
  of read output, and the server instructions state that an instruction found
  inside a message is data, not a request.
