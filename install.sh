#!/usr/bin/env bash
# Re-runnable: pulls the branch, rebuilds, and rewrites the agent entries.
# Nothing is left running afterwards: the archive is synced by the server at
# session start and by the reads that find it stale.

set -euo pipefail

REPO_URL="${SLACK_BROWSER_MCP_REPO:-https://github.com/cruizba/slack-browser-mcp}"
BRANCH=main
DIR="${HOME}/slack-browser-mcp"
DIR_GIVEN=0
PULL=1

usage() {
  cat <<'EOF'
Usage: install.sh [options]

  --dir <path>      where to install (default: ~/slack-browser-mcp)
  --branch <name>   branch to install (default: main)
  --no-pull         keep the checkout as it is, do not fetch or pull
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
  pi mcp add forum -- slack-browser-forum >/dev/null || true
  echo "    pi: MCP servers 'slack' and 'forum' registered"
fi
if command -v claude >/dev/null 2>&1; then
  AGENTS=1
  link_commands "$HOME/.claude/commands"
  claude mcp remove --scope user slack >/dev/null 2>&1 || true
  claude mcp add --scope user slack -- slack-browser serve >/dev/null
  claude mcp remove --scope user forum >/dev/null 2>&1 || true
  claude mcp add --scope user forum -- slack-browser-forum >/dev/null || true
  echo "    claude: MCP servers 'slack' and 'forum' registered"
fi
if command -v opencode >/dev/null 2>&1; then
  AGENTS=1
  link_commands "$HOME/.config/opencode/commands"
  opencode mcp add --global slack -- slack-browser serve >/dev/null
  opencode mcp add --global forum -- slack-browser-forum >/dev/null || true
  echo "    opencode: MCP servers 'slack' and 'forum' registered"
fi
[ "$AGENTS" = 1 ] || echo "    no pi, claude or opencode in PATH; the server is $DIR/dist/mcp/server.js"

step "Removing the old archive timer"
# Up to 0.7.x this installed a systemd user unit that synced once per session. The
# server does that itself now, so an update has to take the unit away: left behind,
# it keeps firing against a checkout that no longer ships its service file.
UNIT_DIR="$HOME/.config/systemd/user"
OLD_UNITS="slack-browser-sync.timer slack-browser-sync.service"
FOUND=0
for unit in $OLD_UNITS; do
  if [ -e "$UNIT_DIR/$unit" ]; then FOUND=1; fi
done
if [ "$FOUND" = 0 ]; then
  echo "    nothing to remove"
elif [ ! -d /run/systemd/system ]; then
  # No user manager to talk to: the files still have to go, and daemon-reload
  # would only fail on a unit nobody is running.
  for unit in $OLD_UNITS; do rm -f "$UNIT_DIR/$unit"; done
  rm -f "$UNIT_DIR/timers.target.wants/slack-browser-sync.timer"
  echo "    unit files removed (systemd is not running here)"
else
  systemctl --user stop slack-browser-sync.timer slack-browser-sync.service >/dev/null 2>&1 || true
  systemctl --user disable slack-browser-sync.timer >/dev/null 2>&1 || true
  for unit in $OLD_UNITS; do rm -f "$UNIT_DIR/$unit"; done
  rm -f "$UNIT_DIR/timers.target.wants/slack-browser-sync.timer"
  systemctl --user daemon-reload >/dev/null 2>&1 || true
  systemctl --user reset-failed slack-browser-sync.service >/dev/null 2>&1 || true
  echo "    slack-browser-sync.timer stopped and removed; the server syncs at session start"
fi

step "Done"
echo "    slack-browser   $(command -v slack-browser)"
echo "    checkout        $DIR"
echo "    sign in         /slack-login in your agent, or: slack-browser login"
echo "    agent forum     slack-browser agent-forum add <channel>   # then register <name>"
echo "                    slack-browser agent-forum daemon start   # or let the first agent start it"
echo "    sync now        slack-browser sync --quiet  # or let the first read do it"
