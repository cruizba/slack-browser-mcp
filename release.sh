#!/usr/bin/env bash
# Cut a release: bump the version, commit it, tag it, push and open the GitHub
# release. The tag is what `slack-browser update` installs; main is development.

set -euo pipefail

BRANCH=main
PUSH=1
NOTES=1
DRY=0
WANTED=""

usage() {
  cat <<'EOF'
Usage: release.sh <version> [options]

  <version>      0.2.0, or a bump: major, minor, patch (a prerelease is written
                 out in full: 0.3.0-rc.1)
  --no-push      bump, commit and tag locally; print the push command
  --no-notes     skip `gh release create`
  --dry-run      say what would happen and change nothing
  -h, --help     show this help
EOF
}

die() {
  printf 'release.sh: %s\n' "$*" >&2
  exit 1
}

step() {
  printf '\n==> %s\n' "$*"
}

while [ $# -gt 0 ]; do
  case "$1" in
    --no-push) PUSH=0; shift ;;
    --no-notes) NOTES=0; shift ;;
    --dry-run) DRY=1; shift ;;
    -h|--help) usage; exit 0 ;;
    -*) usage >&2; die "unknown option: $1" ;;
    *)
      [ -z "$WANTED" ] || die "give one version"
      WANTED="$1"
      shift
      ;;
  esac
done
[ -n "$WANTED" ] || { usage >&2; die "give a version, or major, minor, patch"; }
for tool in git node npm; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done

step "Checking the checkout"
[ -d .git ] || die "run this from the checkout"
[ -z "$(git status --porcelain)" ] || die "the checkout has local changes; commit or stash them first"
CURRENT="$(git rev-parse --abbrev-ref HEAD)"
[ "$CURRENT" = "$BRANCH" ] || die "cut releases from $BRANCH, not $CURRENT"
git fetch --quiet origin "$BRANCH"
[ "$(git rev-parse HEAD)" = "$(git rev-parse "origin/$BRANCH")" ] \
  || die "$BRANCH and origin/$BRANCH differ; pull or push first"

step "Resolving the version"
NEXT="$(node -e '
const fs = require("node:fs")
const wanted = process.argv[1]
const current = JSON.parse(fs.readFileSync("package.json", "utf8")).version
const [major, minor, patch] = current.split("-")[0].split(".").map(Number)
let next
if (wanted === "major") next = `${major + 1}.0.0`
else if (wanted === "minor") next = `${major}.${minor + 1}.0`
else if (wanted === "patch") next = `${major}.${minor}.${patch + 1}`
else if (/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(wanted)) next = wanted
else process.exit(2)
process.stdout.write(next)
' "$WANTED")" || die "not a version: $WANTED"
TAG="v$NEXT"
echo "    $CURRENT is $(git rev-parse --short HEAD); next release is $TAG"

if [ "$DRY" = 1 ]; then
  echo "    would run: npm version $NEXT -m 'chore: release v%s'"
  [ "$PUSH" = 1 ] && echo "    would push: origin $BRANCH $TAG"
  [ "$PUSH" = 1 ] && [ "$NOTES" = 1 ] && echo "    would run: gh release create $TAG --generate-notes"
  exit 0
fi

step "Bumping and tagging"
npm version "$NEXT" -m "chore: release v%s"
TAGGED="$(git describe --tags --exact-match HEAD)"
[ "$TAGGED" = "$TAG" ] || die "git tagged $TAGGED, expected $TAG"
echo "    $TAG at $(git rev-parse --short HEAD)"

SLUG="$(git remote get-url origin | sed -n 's#.*github\.com[:/]\(.*\)\.git$#\1#p')"
RELEASES_URL=""
[ -n "$SLUG" ] && RELEASES_URL="https://github.com/$SLUG/releases"

step "Pushing"
if [ "$PUSH" = 0 ]; then
  echo "    skipped: git push origin $BRANCH $TAG"
else
  git push origin "$BRANCH" "$TAG"
  echo "    $BRANCH and $TAG pushed"
fi

step "Release notes"
if [ "$PUSH" = 1 ] && [ "$NOTES" = 1 ] && command -v gh >/dev/null 2>&1; then
  if gh release create "$TAG" --generate-notes --title "$TAG"; then
    echo "    release $TAG published"
  else
    echo "    gh could not create the release; the tag is already pushed"
    [ -n "$RELEASES_URL" ] && echo "    retry at $RELEASES_URL/new?tag=$TAG"
  fi
else
  if [ "$PUSH" = 0 ]; then echo "    skipped: --no-push"
  elif [ "$NOTES" = 0 ]; then echo "    skipped: --no-notes"
  else echo "    gh is not in PATH"
  fi
  [ -n "$RELEASES_URL" ] && echo "    notes: $RELEASES_URL/new?tag=$TAG"
fi

step "Done"
echo "    tag      $TAG"
echo "    update   slack-browser update"
