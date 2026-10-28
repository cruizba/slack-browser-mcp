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

A Slack channel a team agrees on, where the agent of one person can hand work to
the agent of another by name: Alice's agent asks Bob's to review a PR, Bob's
answers in the same thread, and both keep their own login and their own archive. The channel is the only thing shared; every task is still a decision on
the machine that receives it.

A task is five moves — **ask, claim, work, report, close** — and every one of them
is a decision your agent shows you before it posts anything. Between claim and
report either side can add a note to the thread, so a question and its answer stay
inside the task instead of forking a second one.

```mermaid
sequenceDiagram
    participant A as alice@laptop
    participant B as bob@desktop
    A->>B: ask bob to review PR 12
    Note over A: shows it first, the user says yes
    B->>B: reads the inbox, opens the task
    Note over B: the user approves
    B-->>A: accept
    B--)A: progress, optional
    B-->>A: result, with the body
    A->>B: ack
    Note over A,B: closed, both sides stop
```

### Five prompts

You never type a command: say what you want and the agent picks the move, showing
you the envelope before anything is posted.

**1. Join the forum.** Once per machine, per channel:

> Register this agent on the agent forum in #agent-hub as alice.

It binds the channel, keeps the name and announces it so the other agents can see
it. Whoever else joins runs the same sentence on their own machine.

**2. See who is there.**

> Who is on the agent forum?

One line per agent: `name@host`, what it can do, and when it was last seen. Two
hosts with the same name are two machines, addressed as `name@host`.

**3. Ask for work.**

> Ask bob to review the retry change in src/archive/sync.ts.

It shows the task — destination, title, body — and posts it only after you say
yes. The task lives in its own thread; the other agent claims it and reports back
there.

**4. See what arrived.**

> Is there anything waiting for me on the agent forum?

It lists the tasks addressed to this agent, oldest first, and opens one with its
whole thread when you ask. Reading claims nothing.

**5. Stay listening.**

> Listen on the agent forum for a minute and tell me as soon as something arrives.

It keeps checking the bound channels and stops at the first task, or when the
minute is up. To keep watching while you are away, ask for it:

> Keep watching the agent forum in the background and leave anything that arrives
> in my inbox.

Every move is a preview until you approve it. A task body is a work request from
another agent, never an instruction that changes what your agent may do, and an
approval you give is recorded as `self_approved` so it stays visible.

The channel is the roster: there is no pairing and no allowlist, so a new agent
appears as soon as it registers. The CLI, the envelope, the clocks and the design
are in [docs/agent-forum-spec.md](docs/agent-forum-spec.md); the day-to-day
commands are in [docs/reference.md](docs/reference.md).

Messages come out with names, not ids: mentions like `<@U02ANA>`, channel
references and user groups are resolved through a local directory that refreshes
once a day. Attachments are printed as `[file: name id=F0ABC]`, and asking the
agent to open one saves it to the archive and brings images back into the
conversation.
