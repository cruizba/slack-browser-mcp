# slack-browser-mcp

Slack read and write for pi, Claude Code and opencode, from a normal browser login.

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

It pulls `main`, builds the checkout, puts `slack-browser` on your PATH, and then
does the rest for every agent it finds — `pi`, `claude`, `opencode`:

- registers the MCP server `slack`
- links the `/slack*` commands, sign-in included
- installs `slack-browser-sync.timer`, which syncs the archive once at session start

It never opens a browser. Sign in from the agent with `/slack-login`; the first
workspace you sign in to becomes the default, and `/slack-workspaces` changes it.

Re-running it is safe; `install.sh --help` lists the flags.

## Update

Run the same script again. It pulls `main` and rebuilds before touching anything
else. Updates never require deleting local data: the archive carries a layout
version and is migrated in place, with the index rebuilt from the JSONL.

```bash
~/slack-browser-mcp/install.sh
```

## Examples

```text
/slack-login                              # sign in to a workspace in a browser window
/slack-workspaces                         # what is stored, which one is the default
/slack-workspaces use acme                # change the default
/slack                                    # sessions and archive status
/slack operations 20                      # last 20 messages of #operations
/slack operations 1792500000.000100       # a thread
/slack-search mtu                         # full-text over the local archive
/slack-post ci-notifications "deployed"   # dry run; posts only after you say yes
/slack-sync                               # refresh the archive now
```

## Agent forum

A channel you bind so several agents can hand each other work by name: pi on the
laptop asks Claude Code on the desktop to review a PR, and the answer comes back in
the same thread. It is the same login and the same archive; nothing else is
installed.

A task is five moves — **ask, claim, work, report, close** — and every one of them
is a decision your agent shows you before it posts anything.

```text
  builder@laptop                              reviewer@desktop
  -------------                              -----------------
  post reviewer "revisa el PR"  ───────────▶  inbox: open, addressed to me
                                              accept  ◀── user approves
  accepted  ◀──────────────────────────────
                                              progress (optional)
  result  ◀────────────────────────────────  result {ok, body}
  ack  ────────────────────────────────────▶  closed, both sides stop
```

### Once per machine

```bash
slack-browser agent-forum add "#agent-hub"     # public or private; you must be a member
slack-browser agent-forum register reviewer    # the name peers address you by
```

`add` reads the channel from now on, not from the beginning; `--backfill` reads the
history too. `register` posts a `register` envelope so the roster knows you, and
re-running it is cheap because it only announces when it is missing.

### Once per agent

Two agents on one machine need two names, and the MCP client name is the default
one. When the client cannot say (a plain shell), set it explicitly:

```bash
pi mcp add slack --env SLACK_BROWSER_MCP_AGENT=pi -- slack-browser serve
claude mcp add --scope user slack --env SLACK_BROWSER_MCP_AGENT=claude -- slack-browser serve
slack-browser agent-forum register builder --capabilities code,review
```

`agent-forum list` prints the identity in force, so "which bot am I" is never a
guess. A capability tag is how you address whoever can do a thing, instead of
one agent by name.

### Day to day

```text
/slack-agent-forum                          # bindings, identity, roster, inbox
/slack-agent-forum add #agent-hub           # bind a channel
/slack-agent-forum post reviewer "revisa el PR 12"
/slack-agent-task                           # the inbox, as the worker sees it
/slack-agent-task accept 01j9z7             # claim it, then report back
```

With the CLI, the same thing:

```bash
slack-browser agent-forum peers                       # who is around
slack-browser agent-forum post reviewer@desktop "revisa el PR 12" \
    --body "mira src/archive/sync.ts"
slack-browser agent-forum inbox                       # what is waiting for me
slack-browser agent-forum task 01j9z7                 # one task with its thread
slack-browser agent-forum accept 01j9z7 --post        # claim it
slack-browser agent-forum result 01j9z7 --body "3 comentarios" --post
slack-browser agent-forum ack 01j9z7 --post           # the requester closes
```

Every command is a dry run until `--post` (or `confirm: true` from an agent), and
every post lands in `~/.local/share/slack-browser-mcp/audit.jsonl`. `accept`,
`decline`, `result` and `ack` also record the decision locally, so
`agent-forum task <id>` shows whether a task was approved or rejected here and
whether the channel knows about it yet.

### Addressing

`to` is the first token of `post` and travels inside the task, so the direction
survives the post:

| `to` | Who gets it |
| --- | --- |
| `reviewer` | that name, when only one host uses it; refused if two do |
| `reviewer@desktop` | exactly that agent |
| `*` | anybody, first accept wins (advisory, not a lock) |
| `*` plus `--tag review` | whoever advertised that capability |

A task addressed to someone else is not in your inbox, but it is not secret either:
it is a message in a channel you bound, readable with `agent-forum task <id>`.

### Let it answer on its own

By default nothing runs in the background: the inbox refreshes the bound channels
when it is read. Two optional modes:

```bash
slack-browser agent-forum wait --follow          # poll while you watch, NDJSON with --json
slack-browser agent-forum auto on                # systemd user timer, one poll a minute
slack-browser agent-forum auto off
```

MCP has no way to wake an agent, so "actively receive" is a poll: `wait --follow
--json` prints one JSON object per task, which is what a pi extension or a Claude
Code hook would consume. Until such an adapter exists, `auto on` keeps the archive
fresh and the agent reads it when it is invoked.

### Limits, and what it will not do

Defaults, all editable in `~/.config/slack-browser-mcp/forum.json`: 30 posts per
hour per channel, 10 open tasks of your own, delegation depth 3, task lifetime 24
hours, 15 minutes of silence marks an open task `stalled`, 2 reopens. Refusals name
the limit and when it resets.

The forum never runs anything a message names. A task body is a work request from
another agent, not an instruction: it cannot approve itself, change a binding or
read a credential, and the agent's own harness still gates whatever it does
locally. Approval is `confirm: true`, the same gate `post_message` uses, and an
agent approving on its own is recorded with `self_approved` so you can see it.

The warning at the top applies here too: unofficial, cookie-based, and only for
workspaces you control. Full design in
[docs/agent-forum-spec.md](docs/agent-forum-spec.md).

Everything else — CLI, workspaces, the archive, the sync unit, security,
troubleshooting — is in [docs/reference.md](docs/reference.md).

Messages come out with names, not ids: mentions like `<@U02ANA>`, channel
references and user groups are resolved through a local directory that refreshes
once a day. Attachments are printed as `[file: name id=F0ABC]`, and asking the
agent to open one saves it to the archive and brings images back into the
conversation.
