# slack-browser-mcp

Slack read and write for pi, Claude Code and opencode, from a normal browser login.

**Linux only.** The archive timer is a systemd user unit; there is no fallback for
macOS, WSL without systemd, or other init systems. Needs Node.js 22 or newer and
Google Chrome.

## Install

```bash
git clone https://gitea.infra.cruizba.dev/cruizba/slack-browser-mcp ~/slack-browser-mcp && ~/slack-browser-mcp/install.sh
```

It pulls `main`, builds the checkout, puts `slack-browser` on your PATH, and then
does the rest for every agent it finds — `pi`, `claude`, `opencode`:

- registers the MCP server `slack`
- links the `/slack*` commands, sign-in included
- installs `slack-browser-sync.timer`, which refreshes the archive every 15 minutes

It never opens a browser. Sign in from the agent with `/slack-login`; the first
workspace you sign in to becomes the default, and `/slack-workspaces` changes it.

Re-running it is safe; `install.sh --help` lists the flags.

## Update

Run the same script again. It pulls `main` and rebuilds before touching anything
else:

```bash
~/slack-browser-mcp/install.sh
```

## Examples

```text
/slack-login                              # sign in to a workspace in a browser window
/slack-workspaces                         # what is stored, which one is the default
/slack-workspaces use openvidu            # change the default
/slack                                    # sessions and archive status
/slack operations 20                      # last 20 messages of #operations
/slack operations 1792500000.000100       # a thread
/slack-search mtu                         # full-text over the local archive
/slack-post ci-notifications "deployed"   # dry run; posts only after you say yes
/slack-sync                               # refresh the archive now
```

Everything else — CLI, workspaces, the archive, the sync timer, security,
troubleshooting — is in [docs/reference.md](docs/reference.md).
