#!/bin/sh
# Installs the release binary and registers it with every agent it can find.
#
# Re-runnable: it replaces the binary in place, and it re-registers the MCP servers
# so an upgrade does not leave an agent pointing at a stale path.
#
# The download is verified against the published checksum before anything is
# installed. An installer that runs whatever it fetched is a supply chain with no
# lock on it.

set -eu

REPO="${SLACK_BROWSER_MCP_REPO:-cruizba/slack-browser-mcp}"
BASE="${SLACK_BROWSER_MCP_BASE:-https://github.com/${REPO}/releases}"
BIN_DIR="${SLACK_BROWSER_MCP_BIN_DIR:-${HOME}/.local/bin}"
SHARE_DIR="${SLACK_BROWSER_MCP_SHARE_DIR:-${HOME}/.local/share/slack-browser-mcp}"
TAG=""

usage() {
  cat <<'EOF'
Usage: install.sh [options]

  --tag <vX.Y.Z>   install that release instead of the latest
  --bin <path>     where the binary goes (default: ~/.local/bin)
  -h, --help       show this help

Environment:
  SLACK_BROWSER_MCP_REPO       owner/name on GitHub
  SLACK_BROWSER_MCP_BASE       releases base URL (for a mirror)
  SLACK_BROWSER_MCP_BIN_DIR    where the binary goes
  SLACK_BROWSER_MCP_SHARE_DIR  where the slash commands go
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
    --tag) TAG="$2"; shift 2 ;;
    --bin) BIN_DIR="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; die "unknown option: $1" ;;
  esac
done

need() {
  command -v "$1" >/dev/null 2>&1 || die "$1 is required and is not in PATH"
}

need curl
need tar
need uname

step "Detecting this machine"
os=$(uname -s)
case "$os" in
  Linux) goos=linux ;;
  Darwin) goos=darwin ;;
  *) die "unsupported system: $os (Windows has install.ps1)" ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64|amd64) goarch=amd64 ;;
  arm64|aarch64) goarch=arm64 ;;
  *) die "unsupported architecture: $arch" ;;
esac
printf '    %s/%s\n' "$goos" "$goarch"

if [ -z "$TAG" ]; then
  step "Finding the latest release"
  # The redirect the releases page leaves behind names the newest tag, which avoids
  # needing a token or the API's rate limit.
  latest=$(curl -fsSL -o /dev/null -w '%{url_effective}' "${BASE}/latest" 2>/dev/null || true)
  TAG=${latest##*/}
  [ -n "$TAG" ] && [ "$TAG" != "latest" ] || die "could not read the latest release tag from ${BASE}/latest"
  printf '    %s\n' "$TAG"
fi

version=${TAG#v}
archive="slack-browser-mcp_${version}_${goos}_${goarch}.tar.gz"
url="${BASE}/download/${TAG}/${archive}"
checksums_url="${BASE}/download/${TAG}/slack-browser-mcp_${version}_checksums.txt"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

step "Downloading ${TAG}"
curl -fsSL -o "${tmp}/${archive}" "$url" || die "cannot download ${url}"
curl -fsSL -o "${tmp}/checksums.txt" "$checksums_url" || die "cannot download ${checksums_url}"

step "Verifying the checksum"
want=$(awk -v name="$archive" '$2 == name || $2 == "*" name { print $1 }' "${tmp}/checksums.txt")
[ -n "$want" ] || die "the checksums for ${TAG} do not mention ${archive}"

if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "${tmp}/${archive}" | awk '{ print $1 }')
else
  got=$(shasum -a 256 "${tmp}/${archive}" | awk '{ print $1 }')
fi

[ "$got" = "$want" ] || die "the download does not match its published checksum; nothing was installed"
printf '    ok\n'

step "Installing"
mkdir -p "$BIN_DIR" "$SHARE_DIR"
tar -xzf "${tmp}/${archive}" -C "$tmp"

# Both entry points are real binaries in the archive: the forum surface has its own main
# package, so a copy of the CLI under that name would answer the wrong catalogue.
for name in slack-browser slack-browser-forum; do
  for candidate in "$tmp"/*/"$name" "$tmp"/"$name" "$tmp"/*/"$name.exe" "$tmp"/"$name.exe"; do
    if [ -f "$candidate" ]; then
      cp "$candidate" "${BIN_DIR}/.${name}.new"
      chmod 755 "${BIN_DIR}/.${name}.new"
      mv "${BIN_DIR}/.${name}.new" "${BIN_DIR}/${name}"
      break
    fi
  done
  [ -x "${BIN_DIR}/${name}" ] || die "the archive did not carry a ${name} binary"
  printf '    %s\n' "${BIN_DIR}/${name}"
done

if [ -d "$tmp/commands" ]; then
  mkdir -p "$SHARE_DIR/commands"
  cp "$tmp"/commands/*.md "$SHARE_DIR/commands/" 2>/dev/null || true
fi

step "Registering with the agents in PATH"
agents=0
link_commands() {
  target=$1
  [ -d "$SHARE_DIR/commands" ] || return 0
  mkdir -p "$target"
  for file in "$SHARE_DIR"/commands/*.md; do
    [ -f "$file" ] || continue
    ln -sf "$file" "$target/$(basename "$file")" 2>/dev/null || true
  done
}

if command -v pi >/dev/null 2>&1; then
  agents=1
  pi mcp remove slack >/dev/null 2>&1 || true
  pi mcp add slack -- "${BIN_DIR}/slack-browser" serve >/dev/null
  pi mcp remove forum >/dev/null 2>&1 || true
  pi mcp add forum -- "${BIN_DIR}/slack-browser-forum" >/dev/null || true
  link_commands "$HOME/.pi/agent/commands"
  printf '    pi: MCP servers '"'"'slack'"'"' and '"'"'forum'"'"' registered\n'
fi

if command -v claude >/dev/null 2>&1; then
  agents=1
  claude mcp remove --scope user slack >/dev/null 2>&1 || true
  claude mcp add --scope user slack -- "${BIN_DIR}/slack-browser" serve >/dev/null
  claude mcp remove --scope user forum >/dev/null 2>&1 || true
  claude mcp add --scope user forum -- "${BIN_DIR}/slack-browser-forum" >/dev/null || true
  link_commands "$HOME/.claude/commands"
  printf '    claude: MCP servers '"'"'slack'"'"' and '"'"'forum'"'"' registered\n'
fi

if command -v opencode >/dev/null 2>&1; then
  agents=1
  opencode mcp add --global slack -- "${BIN_DIR}/slack-browser" serve >/dev/null || true
  opencode mcp add --global forum -- "${BIN_DIR}/slack-browser-forum" >/dev/null || true
  link_commands "$HOME/.config/opencode/commands"
  printf '    opencode: MCP servers '"'"'slack'"'"' and '"'"'forum'"'"' registered\n'
fi

if [ "$agents" = 0 ]; then
  printf '    no pi, claude or opencode in PATH; register the server by hand:\n'
  printf '      %s serve\n' "${BIN_DIR}/slack-browser"
fi

case ":${PATH}:" in
  *":${BIN_DIR}:"*) ;;
  *)
    printf '\n    %s is not in PATH; add it to your shell profile\n' "$BIN_DIR"
    ;;
esac

step "Next"
printf '    sign in         %s login\n' "${BIN_DIR}/slack-browser"
printf '    check it        %s status\n' "${BIN_DIR}/slack-browser"
printf '    fill history    %s sync\n' "${BIN_DIR}/slack-browser"
printf '    bind a forum    %s agent-forum add <channel>   # then register <name>\n' "${BIN_DIR}/slack-browser"
printf '\n    restart your agent so it loads the new MCP server\n'
