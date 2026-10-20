# slack-browser-mcp

Slack read and write for pi, Claude Code and opencode, from a normal browser login.

## Install

Pick your agent and paste its one-liner. Everything is installed and registered
by the time it finishes; `slack-browser login` opens Chrome at the end.

**pi**

```bash
git clone https://gitea.infra.cruizba.dev/cruizba/slack-browser-mcp ~/slack-browser-mcp \
  && cd ~/slack-browser-mcp && npm install && npm run build && npm link \
  && mkdir -p ~/.pi/agent/prompts && ln -sf ~/slack-browser-mcp/commands/slack*.md ~/.pi/agent/prompts/ \
  && pi mcp add slack -- slack-browser serve \
  && slack-browser login
```

**Claude Code**

```bash
git clone https://gitea.infra.cruizba.dev/cruizba/slack-browser-mcp ~/slack-browser-mcp \
  && cd ~/slack-browser-mcp && npm install && npm run build && npm link \
  && mkdir -p ~/.claude/commands && ln -sf ~/slack-browser-mcp/commands/slack*.md ~/.claude/commands/ \
  && claude mcp add --scope user slack -- slack-browser serve \
  && slack-browser login
```

**opencode**

```bash
git clone https://gitea.infra.cruizba.dev/cruizba/slack-browser-mcp ~/slack-browser-mcp \
  && cd ~/slack-browser-mcp && npm install && npm run build && npm link \
  && mkdir -p ~/.config/opencode/commands && ln -sf ~/slack-browser-mcp/commands/slack*.md ~/.config/opencode/commands/ \
  && opencode mcp add --global slack -- slack-browser serve \
  && slack-browser login
```

## Examples

```text
/slack                                    # sessions and archive status
/slack operations 20                      # last 20 messages of #operations
/slack operations 1792500000.000100       # a thread
/slack-search mtu                         # full-text over the local archive
/slack-post ci-notifications "deployed"   # dry run; posts only after you say yes
/slack-sync                               # refresh the archive now
```

Everything else — CLI, workspaces, the archive, the sync timer, security,
troubleshooting — is in [docs/reference.md](docs/reference.md).
