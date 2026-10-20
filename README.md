# slack-browser-mcp

Slack read and write for pi, Claude Code and opencode, from a normal browser login.

## Install

```bash
git clone https://gitea.infra.cruizba.dev/cruizba/slack-browser-mcp ~/slack-browser-mcp && cd ~/slack-browser-mcp && npm install && npm run build && npm link && slack-browser login
for d in ~/.pi/agent/prompts ~/.claude/commands ~/.config/opencode/commands; do mkdir -p "$d" && ln -sf ~/slack-browser-mcp/commands/slack*.md "$d"/; done
```

## Connect

```bash
pi mcp add slack -- slack-browser serve
```

```bash
claude mcp add --scope user slack -- slack-browser serve
```

```bash
opencode mcp add --global slack -- slack-browser serve
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
