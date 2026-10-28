# slack-browser-mcp reference

Everything the README leaves out. The browser is used once, to sign in: Playwright
captures the two things Slack's own web client uses — the `xoxc` token and the `d`
cookie — and every later call goes straight to `https://slack.com/api`. No DOM
scraping, no bot installation, no admin approval for a workspace app.

Status: read and write, with a local archive. Channels, DMs, history and threads
are read-only surface; `post_message` is the single tool with a side effect, it
refuses to send unless the call carries `confirm=true`, and every send is appended
to an audit log. A plain-text copy of the conversations lives on disk, synced once
per session, so history survives and a read answers from it instead of the API.

## Requirements

- Linux with systemd (user units and `loginctl`). Nothing else is supported.
- Node.js 22 or newer
- Google Chrome. The login starts it as a normal browser and attaches over CDP;
  set `CHROME_PATH` if it is not in a standard location. Playwright's bundled
  Chromium works via `npx playwright install chromium`, but Slack is more likely
  to accept a stock Chrome install.

## install.sh

The README installs with it. It pulls `main`, runs `npm ci` and `npm run build`,
`npm link`, then walks the agents found in `PATH` and, for each, links the
`commands/` slash commands into that agent's directory and registers the MCP
server `slack`. Finally it renders `systemd/slack-browser-sync.service.in` into
`~/.config/systemd/user/`, installs the sync timer, enables it and turns on linger.
It never opens a browser: signing in is a command the agent runs. Re-running it
is safe: it pulls, rebuilds and rewrites what it owns (a dirty checkout is left
on its branch). That re-run is also the update: there is no separate command.

```bash
./install.sh --help

  --dir <path>      where to install (default: ~/slack-browser-mcp)
  --branch <name>   branch to install (default: main)
  --no-pull         keep the checkout as it is, do not fetch or pull
  --no-systemd      skip the archive sync unit
```

## Install by hand

The README has the one-liner. Step by step, it is:

```bash
git clone https://github.com/cruizba/slack-browser-mcp
cd slack-browser-mcp
npm install && npm run build
npm link          # puts `slack-browser` on PATH
```

Without `npm link`, replace `slack-browser serve` below with
`node /path/to/slack-browser-mcp/dist/mcp/server.js`.

## Sign in

From the agent, `/slack-login` (the `login_workspace` tool) opens the browser;
it returns as soon as Chrome is up, and the tool waits up to `wait_seconds` for
the sign-in to finish. The CLI does the same thing and blocks:

```bash
slack-browser login
```

Either way Chrome opens on `app.slack.com`. Sign in as you normally would, including SSO or
2FA. The tool waits until it can read the session, proves it with `auth.test`,
and only then writes it to disk. Sign in **one workspace at a time**; a workspace
whose stored cookie Slack rejects is reported instead of saved.

If the sign-in is refused with "This browser or app may not be secure", that is
Slack's automation check. The login does not use an automation-driving browser
for exactly this reason: it launches Chrome itself, without Playwright's
`--enable-automation` flag, and attaches over the DevTools protocol. If it still
happens, the failure screenshot is at `/tmp/slack-browser-login-failure.png`.

The first workspace stored becomes the default automatically. Later sign-ins
leave it alone; `/slack-workspaces` lists what is stored and changes it.

One sign-in at a time: a lock directory (`login.lock` in the data directory)
makes a second attempt report the running one instead of opening a second
browser. A login the agent starts writes its progress to `login.log` next to it,
and the last line of that file is what `list_workspaces` reports when it ends.

Useful flags: `--timeout <seconds>` (default 300), `--headless` (only if your
workspace has no SSO challenge).

## Register the MCP server by hand

The agent talks to one stdio process, `slack-browser serve`, registered as the
server `slack`. The commands in the README write these entries for you:
`~/.pi/agent/mcp.json`, `~/.claude.json` and `~/.config/opencode/opencode.json`.

`mcpServers` is the shape for pi and Claude Code, `mcp` the one for opencode
(2.x nests the servers under `servers`):

```json
{ "mcpServers": { "slack": { "command": "slack-browser", "args": ["serve"] } } }
```

```json
{ "mcp": { "servers": { "slack": { "type": "local", "command": ["slack-browser", "serve"] } } } }
```

Check the connection with `pi mcp list`, `claude mcp list` or `opencode mcp list`;
each should report `slack` as connected with its tools.

## Slash commands

Six prompt templates in `commands/` give the agents `/slack-login`,
`/slack-workspaces`, `/slack`, `/slack-search`, `/slack-post` and `/slack-sync`.
They are plain Markdown with `description` front matter, which is the part all
three agents read; the file name is the command. Link them into each agent's
command directory:

```bash
for dir in ~/.pi/agent/prompts ~/.claude/commands ~/.config/opencode/commands; do
  mkdir -p "$dir"
  ln -sf "$PWD"/commands/slack*.md "$dir"/
done
```

Use `cp` instead of `ln -sf` if you would rather not have the commands follow the
repository. Claude Code and pi pick them up in the next session (`/reload` in
pi), opencode on the next TUI start — `opencode run` passes the text through
unexpanded, so there it is a TUI feature.

| Command | What it does |
| --- | --- |
| `/slack-login [workspace]` | Opens Chrome to sign in; the first workspace becomes the default |
| `/slack-workspaces [use <ws>\|clear]` | Lists the stored workspaces and changes the default |
| `/slack` | Sessions and archive status for every workspace; reads no messages |
| `/slack <channel> [count]` | Recent messages, printed oldest first (`me` is your own DM) |
| `/slack <channel> <thread_ts>` | Every reply in that thread |
| `/slack-search <query> [channel]` | Full-text over the archive, newest hits first |
| `/slack-post <channel> <text>` | Prints the exact message and posts only after you say yes |
| `/slack-sync [workspace]` | Runs a sync and reports the new archive age |

## Check and read from the shell

```bash
slack-browser accounts                                   # what is stored
slack-browser status                                     # is Slack still happy?
slack-browser read general --limit 10                    # a channel by name
slack-browser read general --workspace acme              # ...in another workspace
slack-browser read C0123ABCD --oldest 1729432991.000000  # page back in time
slack-browser thread general 1729432991.123456           # a thread
slack-browser read me --limit 5                          # your own DM
```

Sending is a dry run unless you pass `--send`:

```bash
slack-browser send ci-notifications "deploy finished"            # prints, sends nothing
slack-browser send ci-notifications "deploy finished" --send     # posts
slack-browser send general "ack" --thread 1792500000.000100 --send
```

## Several workspaces

Each stored session is a workspace. `list_workspaces` shows them and
`set_default_workspace` picks one; from the shell:

```bash
slack-browser workspace                   # '*' marks the default workspace
slack-browser workspace use globex        # default for calls without --workspace
slack-browser workspace clear             # back to "the only stored session" rule
```

A call without `--workspace` resolves in this order: the flag, then
`SLACK_BROWSER_MCP_TEAM`, then `default_workspace` in
`~/.config/slack-browser-mcp/config.json`, then the only stored session. With
several sessions and no default the error lists them instead of guessing. MCP
tools take the same reference as their `workspace` argument, so an agent reads
the default unless the user names another workspace.

`sync` is the exception: without `--workspace` it walks every stored workspace
under one deadline, so the run at session start covers them all. `archive search`
and `archive stats` accept `--workspace` too; an unfiltered search prefixes each
hit with the workspace it came from.

## Signing messages as AI-assisted

Every message `post_message` or `slack-browser send` posts is wrapped in a block
that says an AI wrote it. With nothing configured the block names the MCP client
that sent it and the Slack account it went out from:

```text
────────────────────────────────────────

*Message Sent by AI Agent 'pi' of carlos.ruiz*

the message body

────────────────────────────────────────
```

The two lines are Slack `divider` blocks, so they render with margins of their
own and never share a line with the author's name. Text characters cannot do
that: Slack prints the first line of a message immediately after the name, and
it trims both a leading newline and a zero-width space before rendering.

Set a nickname of your own to replace that line:

```bash
slack-browser assist                    # what is in force
slack-browser assist "Qwen3.8 27B"      # your own label
slack-browser assist --clear            # drop it, back to the generated default
```

```text
────────────────────────────────────────

*Message Sent by AI Qwen3.8 27B*

the message body

────────────────────────────────────────
```

The blocks travel with a plain `text` fallback, which is what notifications,
search and clients without block support show; a message longer than 2900
characters keeps the fallback only. The dry run prints that fallback, so a
message of dashes there is the same block without the dividers, and the audit
log counts that text. To post one message bare, pass `--no-assist` on the CLI or
`assist: false` on `post_message`.

The nickname lives in `assisted_nickname` in `config.json`, and
`SLACK_BROWSER_MCP_ASSISTED_NICKNAME` wins over the file; an empty value there
turns signing off for that shell, which is the way to stop signing without
editing each send. A nickname is one line of Slack mrkdwn, so 1-60 characters,
no `*` and no line break: mrkdwn cannot escape `*`, and a break would split the
block. `set_assisted_nickname` does the same from an agent. The rules and the
header count towards Slack's 4000-character limit for the `text` field, so a
text that already fills it fails with that error instead of losing its tail.

A client that does not identify itself, as `slack-browser send` does, is named
`unknown`; an account with no stored handle drops the `of <account>` part rather
than inventing one.

## Archive: a local copy of your Slack

`sync` copies messages into `~/.local/share/slack-browser-mcp/archive/`, one JSONL
file per channel, and maintains a SQLite FTS5 index next to it. The JSONL is the
source of truth: the index can be rebuilt from it at any time.

```bash
slack-browser sync                    # incremental; every workspace by default
slack-browser sync --workspace acme   # just one
slack-browser sync --full             # re-fetch the backfill window
slack-browser sync --since 400        # widen the first backfill past its 90 days
slack-browser sync --full --since 3000  # as far back as Slack will serve
slack-browser sync --quiet            # no per-channel progress on stderr
slack-browser archive search "mtu"               # full-text, ranked, all workspaces
slack-browser archive search "mtu" --channel operations
slack-browser archive search "mtu" --workspace globex
slack-browser archive stats
slack-browser archive stats --workspace globex
slack-browser archive reindex         # rebuild the index from the JSONL files
slack-browser archive migrate         # upgrade the stored layout in place
```

A workspace seen for the first time is read back **90 days**
(`DEFAULT_BACKFILL_DAYS`): that is where the workspaces handled so far stop
serving channel history, so a narrower window would leave a hole, and a wider one
spends pages Slack ends early anyway. `--since` widens it, `--full` re-walks it.

One pass stops after 4000 messages per channel, newest first, so a channel with
more history than that hits a ceiling before the window's start. `archive stats`
and `archive_status` show where the floor really is, and `archive_messages` says
so in the answer instead of passing a sliced archive off as the whole story.

The archive records its own layout version in `archive/schema.json`. After an
update you do not need to delete anything: `sync` and the archive tools migrate
the files in place first, under the same lock as the sync, and the FTS index is
rebuilt from the JSONL. Data written by a **newer** version is refused with a
message to update `slack-browser`, never read with wrong assumptions. A line that
cannot be parsed is moved to `<channel>.jsonl.corrupt` instead of blocking the
upgrade.

Once per session, through the systemd user units in `systemd/`. `install.sh` does
this for you; by hand it is:

```bash
sed -e "s|@NODE@|$(command -v node)|" -e "s|@REPO@|$PWD|" \
  systemd/slack-browser-sync.service.in > ~/.config/systemd/user/slack-browser-sync.service
install -m 0644 systemd/slack-browser-sync.timer ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now slack-browser-sync.timer
loginctl enable-linger "$USER"        # keep syncing while logged out
systemctl --user list-timers slack-browser-sync.timer
systemctl --user start slack-browser-sync.service   # run one now
```

The timer uses `OnBootSec=2min`, counted from the moment the user manager starts,
so a fresh login or a reboot syncs 2 minutes later. There is no schedule: while you
use the archive, reads keep it current, and anything they had to fetch from Slack
is written back.

**One writer at a time.** Every entry point takes a lock directory
(`archive/.sync.lock`) before touching the archive; a second run exits with
`another sync is already running` instead of interleaving appends and losing
cursors. A lock left by a dead process is taken over.

**Local first, Slack on a miss.** `read_messages`, `get_thread`,
`list_conversations` and `search_messages` answer from disk whenever they can, and
say where the answer came from: `archive: 3 min ago` or `from Slack`. A read that
finds nothing cached asks Slack and files the answer, so the next call is local:
`read_messages` for a channel the archive never saw, `get_thread` for a thread with
replies still missing, and `search_messages` for a query with no local hit, which
falls back to Slack's own search. Search hits are archived too, but marked
`source: search`: they are individual messages, not a window, so they never pass
for channel history until a real fetch confirms them.

**Freshness, not assumptions.** A read never waits for a sync: when the archive is
too old for the question, it spawns a detached `sync --quiet --deadline 180` and
returns what it already has. "Too old" depends on the question. A read that
reaches the newest messages (no `latest`) tolerates 15 minutes, because a day-old
tail would answer the wrong thing. A read that pages back through history only
needs the archive to be from the same day, so it tolerates 24 hours: an older
tail cannot change what happened last week. `get_thread` and `search_messages` are
history reads; a thread that gained replies is a miss anyway, so it goes to Slack
fresh. Both numbers live in `config.json` (`stale_after_minutes`,
`recent_after_minutes`) and in `SLACK_BROWSER_MCP_STALE_AFTER_MINUTES` /
`SLACK_BROWSER_MCP_RECENT_AFTER_MINUTES`. The nudge stamp is `archive/.nudge` and
lives 5 minutes, so a sync that keeps failing cannot turn every read into another
process. `archive_status` prints the policy in force. Set
`SLACK_BROWSER_MCP_NO_AUTOSYNC=1` to disable the nudge entirely.

**One request every 500 ms.** The client is a token bucket: the first 4 calls of a
run go out immediately, so a single interactive tool stays instant, and after that
one token comes back every 500 ms (plus ±20% jitter), which is about 2 requests per
second against Slack's documented "50+ per minute". A 91-channel incremental pass
takes ~50 s instead of ~20 s. A `429` is still honoured through `Retry-After`.
`SLACK_BROWSER_PACE_MS=0` disables pacing, and the value itself is the interval in
milliseconds if you want it slower.

What the archive does **not** promise:

- **Deletions and edits** are invisible to an incremental fetch. Once a day each
  channel is re-read over a 7-day window; whatever disappeared is marked
  `deleted_at`, never erased. Older deletions before the first sync are unknown.
- **Thread replies** cost one API call each. New replies are picked up for parents
  seen in that window, so a busy old thread can lag up to a day. Parents with
  unfetched replies wait in a persisted backlog and drain across runs.
- **Disk is sensitive.** The archive is a plaintext mirror of your DMs and private
  channels: `0700` directories, `0600` files, and it will end up in any full-disk
  backup you run. `rm -rf ~/.local/share/slack-browser-mcp/archive` is a valid
  reset; the next `sync` starts over.

`--workspace <name|domain|team-id>` works on every command that talks to Slack;
see [Several workspaces](#several-workspaces) for how the default is chosen.

## Tools

| Tool | Arguments | Notes |
| --- | --- | --- |
| `auth_status` | `workspace?` | Validates each stored session with `auth.test`. Never returns tokens. |
| `login_workspace` | `workspace?`, `wait_seconds?` | Opens Chrome for a sign-in, detached so no tool call outlives the client timeout. Reports a running sign-in instead of starting a second one. |
| `list_workspaces` | — | Stored workspaces, which one is the default and why, whether a sign-in is running. Local only. |
| `set_default_workspace` | `workspace?`, `clear?` | Writes `default_workspace` in `config.json`, or clears it. |
| `set_assisted_nickname` | `nickname?`, `clear?` | Writes `assisted_nickname` in `config.json`, or clears it and returns to the generated default; with neither, reports the state in force. |
| `list_conversations` | `workspace?`, `query?`, `kind?` (`all\|channel\|private\|dm`), `limit?` | Only conversations the user belongs to. |
| `read_messages` | `workspace?`, `channel`, `limit?`, `oldest?`, `latest?` | Oldest first, raw Slack `ts` preserved for paging. |
| `get_thread` | `workspace?`, `channel`, `thread_ts`, `limit?` | Every reply in the thread. |
| `post_message` | `workspace?`, `channel`, `text`, `thread_ts?`, `confirm`, `assist?=true` | `confirm=true` is required; without it the call returns the exact message and posts nothing. `assist` wraps the text in the AI-assisted block, signed with the configured nickname or the generated default. |
| `read_file` | `workspace?`, `file`, `refresh?` | Saves a Slack file into the archive and returns images as an image block, so the model can see them. `file` is the id printed after `id=` in a message line. |
| `search_messages` | `query`, `workspace?`, `channel?`, `limit?`, `include_deleted?` | Full-text over the local archive. No network; covers what `sync` has fetched. |
| `archive_messages` | `workspace?`, `order?` (`oldest\|newest`), `limit?`, `author?`, `channel?`, `oldest?`, `latest?`, `include_deleted?`, `include_replies?` | One time-ordered walk of the whole archive, oldest or newest first. Strictly local: no Slack request, no directory refresh, no background sync. The answer for "the oldest messages", "my oldest message" or "the first one in #x", where going channel by channel costs a call each. `author` matches the cached directory by name, handle or id. |
| `archive_status` | `workspace?` | Age of the archive, channels, thread backlog, per-channel errors, whether a sync is running now. |

`channel` accepts `#name`, `name`, a DM id (`D…`), a channel id (`C…`), `me`
for your own DM, or the person a DM is with (`dm:Ana Ruiz`, `@ana`). Names are
per workspace; when a name is ambiguous the error lists the candidates. Every
`workspace` argument accepts a name, domain or team id and falls back to the
default described in [Several workspaces](#several-workspaces); `auth_status`
prints which one that is and why, and an unfiltered `search_messages` labels each
hit with its workspace. `search_messages` takes the same forms in `channel`, and
`list_conversations` filters and sorts on the label, so `query=ana` finds a DM.

**How a conversation is labelled.** `#random`, `private:leads`, `dm:Ana Ruiz`,
`group:Ana, Michel`. Slack names channels, not DMs: a DM is whoever is on the
other side, so its label comes from the directory, and a group DM is named after
its members. A person the directory does not know falls back to the raw id rather
than a guessed name.

A listing prints the label followed by the conversation id, `dm:Ana Ruiz
(D01ANA) 2 members`, and that whole line is a valid `channel` in the next call:
the id is taken from it and the label is not re-resolved. The peer id a DM is
archived under (`dm:U02ANA`) is accepted as well, which matters for a message the
archive holds without ever having seen the conversation listed.

**Names instead of ids.** Slack stores a mention as `<@U02ANA>`, a channel as
`<#C01RANDOM>` and a user group as `<!subteam^S0123|@team>`. Message output
resolves all of them, plus links and HTML entities, through
`archive/<team>/directory.json`: a registry built from `users.list`,
`conversations.list` (channels and private channels) and `users.conversations`
(DMs and group DMs), plus `usergroups.list`, refreshed once a day. Unknown ids
fall back to the raw id instead of guessing. The archive keeps the markup, so a
later directory refresh also improves old messages and `search_messages` resolves
names without network.

**Attachments.** In `read_messages` and `get_thread`, every file shows as
`[file: name id=F0ABC]`. The id is what `read_file` takes: it downloads the
original through `files.info` + the authenticated URL, keeps it under
`archive/<team>/files/` and returns images as an image block (up to 4 MB inline;
larger ones come back as a path). Downloads happen on demand, never during `sync`,
so a backfill does not pull every screenshot ever posted.

## Where things live

| Path | Contents |
| --- | --- |
| `~/.config/slack-browser-mcp/config.json` | preferences: `default_workspace`, `stale_after_minutes`, `recent_after_minutes`, `assisted_nickname`, `0600` |
| `~/.config/slack-browser-mcp/accounts/<team_id>.json` | `xoxc` token and `d` cookie, `0600` |
| `~/.local/share/slack-browser-mcp/profile/` | Chrome profile for the login only |
| `~/.local/share/slack-browser-mcp/audit.jsonl` | one line per message sent, `0600` |
| `~/.local/share/slack-browser-mcp/login.lock/` | held while a sign-in is running, `0700` |
| `~/.local/share/slack-browser-mcp/login.log` | progress and errors of the last sign-in, `0600` |
| `~/.local/share/slack-browser-mcp/archive/` | JSONL per channel + `state.json`, `directory.json` and `schema.json`, `0700`/`0600` |
| `~/.local/share/slack-browser-mcp/archive/<team>/files/` | attachments downloaded by `read_file`, `0700`/`0600` |
| `~/.local/share/slack-browser-mcp/archive.sqlite` | FTS5 index, rebuildable, `0600` |

Environment overrides: `SLACK_BROWSER_MCP_CONFIG_DIR` (store directory),
`SLACK_BROWSER_MCP_CONFIG_FILE` (settings file), `SLACK_BROWSER_MCP_TEAM`
(one-off default workspace, wins over `config.json`),
`SLACK_BROWSER_MCP_STALE_AFTER_MINUTES` and `SLACK_BROWSER_MCP_RECENT_AFTER_MINUTES`
(freshness thresholds, win over `config.json`), `SLACK_BROWSER_MCP_ASSISTED_NICKNAME`
(the header nickname; empty disables it), `SLACK_BROWSER_PACE_MS`
(milliseconds between requests), `SLACK_BROWSER_MCP_NO_AUTOSYNC` (`1` disables the
background sync a read may start), `CHROME_PATH` (Chrome binary),
`XDG_CONFIG_HOME`, `XDG_DATA_HOME`.

## Security

- The store refuses to read a file that is not `0600` or not owned by you, and
  says which `chmod` to run.
- Secrets never appear in tool output, errors or logs; messages are redacted on
  the way out (`src/log/redact.ts`).
- Nothing credential-shaped is committed: the store lives outside the repo and
  `accounts/` is ignored.
- Treat a stored account like a password: it can read every private channel you
  can read. `slack-browser` has no capability beyond what your session has.

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| `Refusing to read … permissions are 644` | `chmod 600 <file>` (the store prints the path). |
| `invalid_auth` / `token_revoked` | `slack-browser login` again; Slack invalidates both halves on logout or password change. |
| `No Slack session captured after 300s` | Finish the sign-in, or raise `--timeout`. Headless mode fails when SSO challenges. |
| `No conversation named …` | Run `list_conversations`; channel names are workspace-specific. |
| `Several Slack sessions are stored` | Pass `--workspace`, or run `slack-browser workspace use <name|domain|team-id>` once. |
| `http_429` after retries | User-token rate limits; wait a minute. The client honours `Retry-After`. |
| `nothing sent; add --send to commit` | Expected: `send` is a dry run by default. |

## Caveats

The warning at the top of [README.md](../README.md) applies to everything below: this
is an unofficial tool, it authenticates with a browser session cookie, and it belongs
only on workspaces you control or are authorised to automate.

`xoxc` + `d` is the private API of Slack's web client, not a documented public
surface. It works, it is what this tool is built on, and Slack can change it. For
a workspace where you are an admin, a Slack app with a user token is more stable
and is the better choice; the browser path exists for workspaces where you cannot
install one.

## Development

```bash
npm test          # unit + in-memory MCP tests, no browser needed
npm run build     # dist/
npm run typecheck
```

Layout: `src/auth` (capture, store), `src/slack` (API client, rendering),
`src/mcp` (tool surface), `src/cli.ts` (human entry point), `commands/` (the
agent slash commands). Design notes and the next milestones:
[implementation-spec.md](implementation-spec.md).
