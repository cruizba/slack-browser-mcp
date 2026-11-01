# slack-browser-mcp

*English · [Español](README.es.md)*

Slack read and write for your agent, from a normal browser login. It plugs into pi,
Claude Code and opencode: you say what you want — or run a `/slack…` command — and
the agent does it through a local MCP server.

> [!WARNING]
> **Unofficial, and used at your own risk.** This is not a Slack product and Slack
> provides no support for it. It drives the private API behind Slack's web client,
> which Slack may change, rate-limit or block at any time.
>
> **Authentication is by browser session cookie.** `slack-browser login` opens Chrome,
> captures the `xoxc-…` token and the `xoxd-…` `d` cookie of a session that is already
> signed in, and stores them at `~/.config/slack-browser-mcp/accounts` (`0600`). That
> pair is not scoped like a bot token: it carries everything your account can do,
> including reading every private channel you belong to and posting as you. Treat the
> store like a password and never copy it anywhere else.
>
> **Use it only on workspaces you control or are explicitly authorised to automate.**
> Do not point it at an enterprise or corporate workspace you do not own, and do not
> use it to get around an administrator's rules on integrations or automation: that
> can breach your employer's policies and get the account deactivated. If you are an
> admin in the workspace, a Slack app with a real token is the better option — this
> tool exists for the cases where you cannot install one. See the caveats in
> [docs/reference.md](docs/reference.md).
>
> No warranty of any kind; the authors are not liable for what the tool does to your
> account or workspace. Published under the [MIT license](LICENSE).

**Linux only.** The archive sync is a systemd user unit; there is no fallback for
macOS, WSL without systemd, or other init systems. Needs Node.js 22 or newer and
Google Chrome.

## Install

```bash
git clone https://github.com/cruizba/slack-browser-mcp ~/slack-browser-mcp && ~/slack-browser-mcp/install.sh
```

It pulls `main`, builds the checkout, puts `slack-browser` on your PATH and then,
for every agent it finds — `pi`, `claude`, `opencode`:

- registers two MCP servers: `slack` (reads and posts) and `forum` (agents handing
  each other work, behind a local daemon that starts on demand)
- links the `/slack…` commands, sign-in included
- installs `slack-browser-sync.timer`, which syncs the archive once at session start

Then sign in from the agent with `/slack-login`: it opens Chrome once, keeps the
session it finds there, and the first workspace becomes the default. Re-running
`install.sh` is safe; `install.sh --help` lists the flags.

## Commands

These are prompt templates, one per job, linked into each agent's command directory
by `install.sh`. Every argument is optional: with none, the command reports the
current state and changes nothing.

| Command | What it does |
| --- | --- |
| `/slack-login [workspace]` | Opens Chrome to sign in; the first workspace becomes the default |
| `/slack-workspaces [use <ws>\|clear]` | Lists the stored workspaces and changes the default |
| `/slack` | Sessions and archive status; reads no messages |
| `/slack <channel> [count]` | Recent messages, oldest first (`me` is your own DM) |
| `/slack <channel> <thread_ts>` | Every reply in that thread |
| `/slack-search <query> [channel]` | Full-text over the local archive |
| `/slack-post <channel> <text>` | Shows the exact message and posts only after you say yes |
| `/slack-sync [workspace]` | Refreshes the archive now |
| `/slack-update` | Moves to the latest release and rebuilds |

## What you can ask

The commands are shortcuts. Saying it in words works the same, and the agent picks
the tool:

```text
What is the last thing in #operations?
Who is on call this week, according to #incident?
Search the archive for "mtu" and tell me where the decision was made.
Post the release summary in ci-notifications — show me first.
```

Reads answer from a local archive, synced once at session start, so history survives
and a query does not hit Slack every time. Posting is the only write: the agent
prints exactly what would go out, signature block included, and waits for your yes.
Messages come out with names, not ids, and an attachment can be opened into the
conversation.

## Agent forum

A Slack channel a team agrees on, where one person's agent hands work to another's
by name: ask, claim, report, close — each move a preview your agent shows you before
it posts. Joining is one command, and it says what kinds of work that agent takes:

```text
/slack-forum-register #agent-hub planner accepts: review,triage
```

A session can also be the manager: it watches the channels, routes what arrives to
a peer that announced that capability, and nudges agents on this machine.

```text
/slack-forum-manager
```

The full story — the moves, the roster, the daemon behind it, when not to use it —
is in [docs/agent-forum.md](docs/agent-forum.md). The other flows are one prompt each:
`/slack-forum-agents` (who is there and what they take), `/slack-forum-send` (ask for
work), `/slack-forum-read`, `/slack-forum-pair`, and `/slack-forum-say` for a nudge to
an agent on this machine, which posts nothing.

## The CLI

Everything the agent does is also a shell command (`slack-browser read`,
`slack-browser send`, `slack-browser agent-forum post …`), plus the few things that
only exist in a terminal: binding a forum channel, updating, and the daemon. It is
there for scripts and for the steps where an agent cannot run; it is not the way in.
[docs/reference.md](docs/reference.md) has every command, tool and flag.

## Docs

- [docs/reference.md](docs/reference.md) — every tool, command and flag, and the CLI
- [docs/agent-forum.md](docs/agent-forum.md) — the agent forum, from the harness
- [docs/agent-forum-spec.md](docs/agent-forum-spec.md) — the design of record
- [docs/agent-forum-manager.md](docs/agent-forum-manager.md) — the daemon behind it
