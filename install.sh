#!/usr/bin/env bash
# Linux only: the archive sync is a systemd user unit, with no fallback.
# Re-runnable: pulls the branch, rebuilds, rewrites the units and agent entries.

set -euo pipefail

REPO_URL="${SLACK_BROWSER_MCP_REPO:-https://github.com/cruizba/slack-browser-mcp}"
BRANCH=main
DIR="${HOME}/slack-browser-mcp"
DIR_GIVEN=0
PULL=1
SYSTEMD=1

usage() {
  cat <<'EOF'
Usage: install.sh [options]

  --dir <path>      where to install (default: ~/slack-browser-mcp)
  --branch <name>   branch to install (default: main)
  --no-pull         keep the checkout as it is, do not fetch or pull
  --no-systemd      skip the archive sync unit
  -h, --help        show this help
EOF
}

die() {
  printf 'install.sh: %s\n' "$*" >&2
  exit 1
}

step() {
  printf '\n==> %s\n' "$*"
}

while [ $# -gt 0 ]; do
  case "$1" in
    --dir) DIR="$2"; DIR_GIVEN=1; shift 2 ;;
    --branch) BRANCH="$2"; shift 2 ;;
    --no-pull) PULL=0; shift ;;
    --no-systemd) SYSTEMD=0; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; die "unknown option: $1" ;;
  esac
done

step "Checking the system"
[ "$(uname -s)" = "Linux" ] || die "this installer is Linux only; see docs/reference.md for the manual steps"
for tool in git node npm; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done
NODE_MAJOR="$(node -p 'process.versions.node.split(".")[0]')"
[ "$NODE_MAJOR" -ge 22 ] || die "Node.js 22 or newer is required, found $(node --version)"
if ! command -v google-chrome >/dev/null 2>&1 \
  && ! command -v google-chrome-stable >/dev/null 2>&1 \
  && ! command -v chromium >/dev/null 2>&1 \
  && ! command -v chromium-browser >/dev/null 2>&1; then
  echo "    no Chrome in PATH; the login will need CHROME_PATH or 'npx playwright install chromium'"
fi

SELF_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
if [ -f "$SELF_DIR/package.json" ] && [ -d "$SELF_DIR/src" ] && [ "$DIR_GIVEN" = 0 ]; then
  DIR="$SELF_DIR"
fi
step "Preparing $DIR"
if [ -d "$DIR/.git" ]; then
  echo "    using the existing checkout"
else
  [ -e "$DIR" ] && die "$DIR exists and is not a git checkout"
  git clone --branch "$BRANCH" "$REPO_URL" "$DIR"
fi
if [ "$PULL" = 1 ]; then
  if [ -n "$(git -C "$DIR" status --porcelain)" ]; then
    echo "    local changes in $DIR; not touching the branch"
  else
    git -C "$DIR" fetch --quiet origin "$BRANCH"
    git -C "$DIR" checkout --quiet "$BRANCH"
    git -C "$DIR" merge --quiet --ff-only "origin/$BRANCH"
    echo "    $BRANCH up to date ($(git -C "$DIR" rev-parse --short HEAD))"
  fi
fi

step "Building"
cd "$DIR"
if [ -f package-lock.json ]; then
  npm ci --no-audit --no-fund
else
  npm install --no-audit --no-fund
fi
npm run build
npm link || die "npm link failed; fix the npm prefix permissions and re-run"

link_commands() {
  mkdir -p "$1"
  ln -sf "$DIR"/commands/slack*.md "$1"/
  echo "    commands -> $1"
}

step "Registering with the agents"
AGENTS=0
if command -v pi >/dev/null 2>&1; then
  AGENTS=1
  link_commands "$HOME/.pi/agent/prompts"
  pi mcp add slack -- slack-browser serve >/dev/null
  echo "    pi: MCP server 'slack' registered"
fi
if command -v claude >/dev/null 2>&1; then
  AGENTS=1
  link_commands "$HOME/.claude/commands"
  claude mcp remove --scope user slack >/dev/null 2>&1 || true
  claude mcp add --scope user slack -- slack-browser serve >/dev/null
  echo "    claude: MCP server 'slack' registered"
fi
if command -v opencode >/dev/null 2>&1; then
  AGENTS=1
  link_commands "$HOME/.config/opencode/commands"
  opencode mcp add --global slack -- slack-browser serve >/dev/null
  echo "    opencode: MCP server 'slack' registered"
fi
[ "$AGENTS" = 1 ] || echo "    no pi, claude or opencode in PATH; the server is $DIR/dist/mcp/server.js"

if [ "$SYSTEMD" = 1 ]; then
  step "Installing the archive sync"
  if [ ! -d /run/systemd/system ]; then
    echo "    systemd is not running here; skipped"
  else
    UNIT_DIR="$HOME/.config/systemd/user"
    NODE_BIN="$(command -v node)"
    mkdir -p "$UNIT_DIR"
    RENDERED="$(mktemp)"
    sed -e "s|@NODE@|$(printf '%s' "$NODE_BIN" | sed 's/[&|]/\\&/g')|g" \
        -e "s|@REPO@|$(printf '%s' "$DIR" | sed 's/[&|]/\\&/g')|g" \
        "$DIR/systemd/slack-browser-sync.service.in" > "$RENDERED"
    if grep -q '@NODE@\|@REPO@' "$RENDERED"; then
      rm -f "$RENDERED"
      die "the service template still has placeholders"
    fi
    # rm first: a previous run may have left a symlink into the checkout.
    rm -f "$UNIT_DIR/slack-browser-sync.service" "$UNIT_DIR/slack-browser-sync.timer"
    install -m 0644 "$RENDERED" "$UNIT_DIR/slack-browser-sync.service"
    install -m 0644 "$DIR/systemd/slack-browser-sync.timer" "$UNIT_DIR/slack-browser-sync.timer"
    rm -f "$RENDERED"
    systemctl --user daemon-reload
    systemctl --user enable --now slack-browser-sync.timer >/dev/null
    loginctl enable-linger "$USER" >/dev/null 2>&1 \
      || echo "    could not enable linger; the sync only runs while you are logged in"
    echo "    slack-browser-sync.timer enabled (runs at session start; reads keep it current)"
  fi
fi

step "Done"
echo "    slack-browser   $(command -v slack-browser)"
echo "    checkout        $DIR"
echo "    sign in         /slack-login in your agent, or: slack-browser login"
echo "    agent forum     slack-browser agent-forum add <channel>   # then register <name>"
echo "                    slack-browser agent-forum auto on        # opt-in background poll"
if [ "$SYSTEMD" = 1 ] && [ -f "$HOME/.config/systemd/user/slack-browser-sync.timer" ]; then
  echo "    timer           systemctl --user list-timers slack-browser-sync.timer"
  echo "    sync now        systemctl --user start slack-browser-sync.service"
fi
