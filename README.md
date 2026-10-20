# slack-browser-mcp

Slack read access for any agent, from a normal browser login.

The browser is used once, to sign in. Playwright captures the two things Slack's
own web client uses — the `xoxc` token and the `d` cookie — and every later call
goes straight to `https://slack.com/api`. No DOM scraping, no bot installation,
no admin approval for a workspace app.

Status: read and write, with a local archive. Channels, DMs, history and threads
are read-only surface; `post_message` is the single tool with a side effect, it
refuses to send unless the call carries `confirm=true`, and every send is appended
to an audit log. A systemd timer keeps a plain-text copy of the conversations on
disk so history survives and search does not need the API.

## Requirements

- Node.js 22 or newer
- Google Chrome. The login starts it as a normal browser and attaches over CDP;
  set `CHROME_PATH` if it is not in a standard location. Playwright's bundled
  Chromium works via `npx playwright install chromium`, but Slack is more likely
  to accept a stock Chrome install.

## Install

```bash
npm install
npm run build
npm link          # optional: puts `slack-browser` on PATH
```

## Sign in

```bash
slack-browser login
```

Chrome opens on `app.slack.com`. Sign in as you normally would, including SSO or
2FA. The tool waits until it can read the session, proves it with `auth.test`,
and only then writes it to disk. Sign in **one workspace at a time**; a workspace
whose stored cookie Slack rejects is reported instead of saved.

If the sign-in is refused with "This browser or app may not be secure", that is
Slack's automation check. The login does not use an automation-driving browser
for exactly this reason: it launches Chrome itself, without Playwright's
`--enable-automation` flag, and attaches over the DevTools protocol. If it still
happens, the failure screenshot is at `/tmp/slack-browser-login-failure.png`.

Useful flags: `--timeout <seconds>` (default 300), `--headless` (only if your
workspace has no SSO challenge).

## Check and read

```bash
slack-browser accounts                                   # what is stored
slack-browser status                                     # is Slack still happy?
slack-browser read general --limit 10                    # a channel by name
slack-browser read general --workspace openvidu          # ...in another workspace
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

Each stored session is a workspace. List them, choose a default, or clear it:

```bash
slack-browser workspace                   # '*' marks the default workspace
slack-browser workspace use codeurjc      # default for calls without --workspace
slack-browser workspace clear             # back to "the only stored session" rule
```

A call without `--workspace` resolves in this order: the flag, then
`SLACK_BROWSER_MCP_TEAM`, then `default_workspace` in
`~/.config/slack-browser-mcp/config.json`, then the only stored session. With
several sessions and no default the error lists them instead of guessing. MCP
tools take the same reference as their `workspace` argument, so an agent reads
the default unless the user names another workspace.

`sync` is the exception: without `--workspace` it walks every stored workspace
under one deadline, so a single timer keeps the whole archive current. `archive
search` and `archive stats` accept `--workspace` too; an unfiltered search
prefixes each hit with the workspace it came from.

## Archive: a local copy of your Slack

`sync` copies messages into `~/.local/share/slack-browser-mcp/archive/`, one JSONL
file per channel, and maintains a SQLite FTS5 index next to it. The JSONL is the
source of truth: the index can be rebuilt from it at any time.

```bash
slack-browser sync                    # incremental; every workspace by default
slack-browser sync --workspace openvidu  # just one
slack-browser sync --full             # re-fetch the backfill window
slack-browser sync --since 90         # widen the first backfill to 90 days
slack-browser sync --quiet            # no per-channel progress on stderr
slack-browser archive search "mtu"               # full-text, ranked, all workspaces
slack-browser archive search "mtu" --channel operations
slack-browser archive search "mtu" --workspace codeurjc
slack-browser archive stats
slack-browser archive stats --workspace codeurjc
slack-browser archive reindex         # rebuild the index from the JSONL files
```

Every 15 minutes, via systemd user units kept in this repo:

```bash
ln -sf "$PWD/systemd/slack-browser-sync.service" ~/.config/systemd/user/
ln -sf "$PWD/systemd/slack-browser-sync.timer" ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now slack-browser-sync.timer
loginctl enable-linger "$USER"        # keep syncing while logged out
systemctl --user list-timers slack-browser-sync.timer
systemctl --user start slack-browser-sync.service   # run one now
```

The timer uses `OnCalendar=` with `Persistent=true`, so a run missed because the
machine was off happens once at the next boot instead of being skipped.

**One writer at a time.** Every entry point takes a lock directory
(`archive/.sync.lock`) before touching the archive; a second run exits with
`another sync is already running` instead of interleaving appends and losing
cursors. A lock left by a dead process is taken over.

**Freshness, not assumptions.** `search_messages` starts its answer with
`archive: 3 min ago`, and `archive_status` gives the full picture. When the archive
is older than 15 minutes, a read also spawns a detached
`sync --quiet --deadline 60` and returns the cached results immediately — the
tool call never waits for it. Set `SLACK_BROWSER_MCP_NO_AUTOSYNC=1` to disable
that nudge.

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

## Register with the agents you use

The server speaks MCP over stdio, so any MCP client works. Absolute paths below
assume this repository lives at `~/DevProjects/Personal/slack-browser-mcp`.

**pi** (`pi mcp add`, or `~/.pi/agent/mcp.json`):

```bash
pi mcp add slack -- node ~/DevProjects/Personal/slack-browser-mcp/dist/mcp/server.js
```

```json
{
  "mcpServers": {
    "slack": {
      "command": "node",
      "args": ["/home/carlos/DevProjects/Personal/slack-browser-mcp/dist/mcp/server.js"],
      "description": "Read Slack channels, DMs and threads"
    }
  }
}
```

**Claude Code**:

```bash
claude mcp add slack -- node ~/DevProjects/Personal/slack-browser-mcp/dist/mcp/server.js
```

**opencode** (`~/.config/opencode/opencode.json`):

```json
{
  "mcp": {
    "slack": {
      "type": "local",
      "command": ["node", "/home/carlos/DevProjects/Personal/slack-browser-mcp/dist/mcp/server.js"],
      "enabled": true
    }
  }
}
```

If you ran `npm link`, `slack-browser serve` works as the command everywhere.

## Tools

| Tool | Arguments | Notes |
| --- | --- | --- |
| `auth_status` | `workspace?` | Validates each stored session with `auth.test`. Never returns tokens. |
| `list_conversations` | `workspace?`, `query?`, `kind?` (`all\|channel\|private\|dm`), `limit?` | Only conversations the user belongs to. |
| `read_messages` | `workspace?`, `channel`, `limit?`, `oldest?`, `latest?` | Oldest first, raw Slack `ts` preserved for paging. |
| `get_thread` | `workspace?`, `channel`, `thread_ts`, `limit?` | Every reply in the thread. |
| `post_message` | `workspace?`, `channel`, `text`, `thread_ts?`, `confirm` | `confirm=true` is required; without it the call returns the exact message and posts nothing. |
| `search_messages` | `query`, `workspace?`, `channel?`, `limit?`, `include_deleted?` | Full-text over the local archive. No network; covers what `sync` has fetched. |
| `archive_status` | `workspace?` | Age of the archive, channels, thread backlog, per-channel errors, whether a sync is running now. |

`channel` accepts `#name`, `name`, a DM id (`D…`), a channel id (`C…`), or `me`
for your own DM. Names are per workspace; when a name is ambiguous the error
lists the candidates. Every `workspace` argument accepts a name, domain or team
id and falls back to the default described in
[Several workspaces](#several-workspaces); `auth_status` prints which one that is
and why, and an unfiltered `search_messages` labels each hit with its workspace.

## Where things live

| Path | Contents |
| --- | --- |
| `~/.config/slack-browser-mcp/config.json` | preferences: `default_workspace`, `0600` |
| `~/.config/slack-browser-mcp/accounts/<team_id>.json` | `xoxc` token and `d` cookie, `0600` |
| `~/.local/share/slack-browser-mcp/profile/` | Chrome profile for the login only |
| `~/.local/share/slack-browser-mcp/audit.jsonl` | one line per message sent, `0600` |
| `~/.local/share/slack-browser-mcp/archive/` | JSONL per channel + `state.json`, `0700`/`0600` |
| `~/.local/share/slack-browser-mcp/archive.sqlite` | FTS5 index, rebuildable, `0600` |

Environment overrides: `SLACK_BROWSER_MCP_CONFIG_DIR` (store directory),
`SLACK_BROWSER_MCP_CONFIG_FILE` (settings file), `SLACK_BROWSER_MCP_TEAM`
(one-off default workspace, wins over `config.json`), `CHROME_PATH` (Chrome
binary), `XDG_CONFIG_HOME`, `XDG_DATA_HOME`.

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
`src/mcp` (tool surface), `src/cli.ts` (human entry point). Design notes and the
next milestones: [docs/implementation-spec.md](docs/implementation-spec.md).
