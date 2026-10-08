#!/usr/bin/env bash
# Cut a SynCloud release from this machine: check, test, build everything
# locally into dist/, tag the commit and publish the files as a GitHub
# release. GitHub only hosts the result; nothing is built there.
#
#   scripts/release.sh 0.1.0                # tests, build, tag, publish
#   scripts/release.sh 0.2.0-rc.1           # a prerelease (the beta channel)
#   scripts/release.sh 0.1.0 --dry-run      # tests and build only
#
# Options:
#   --dry-run      build dist/ but do not tag or publish
#   --skip-tests   skip go vet/test (the build still type-checks)
#   --no-images    leave out the PostgreSQL image archives
#   --draft        publish as a draft release
set -euo pipefail
cd "$(dirname "$0")/.."

REPO=${SYNCLOUD_REPO:-ridoysheikh/syncloud}
V=${1:-}; shift || true
V=${V#v}
DRY=0 TESTS=1 IMAGES=1 DRAFT=""
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY=1 ;;
    --skip-tests) TESTS=0 ;;
    --no-images) IMAGES=0 ;;
    --draft) DRAFT=--draft ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
  shift
done

die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }

[[ "$V" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || die "usage: scripts/release.sh X.Y.Z[-pre] [options]"
TAG=v$V

step "Checks"
[ -z "$(git status --porcelain)" ] || die "the working tree has changes; commit or stash them"
git rev-parse -q --verify "refs/tags/$TAG" >/dev/null && die "tag $TAG exists already"
if [ "$DRY" = 0 ]; then
  command -v gh >/dev/null || die "the GitHub CLI (gh) is required"
  gh auth status >/dev/null 2>&1 || die "gh is not signed in (gh auth login)"
  gh release view "$TAG" -R "$REPO" >/dev/null 2>&1 && die "release $TAG exists on GitHub already"
fi
grep -q "^## \[$V\]" CHANGELOG.md || die "CHANGELOG.md has no \"## [$V]\" section"
echo "releasing $TAG from $(git rev-parse --short HEAD) on $(git rev-parse --abbrev-ref HEAD)"

if [ "$TESTS" = 1 ]; then
  step "Tests"
  test -z "$(gofmt -l cmd internal sdk tools)" || die "gofmt: $(gofmt -l cmd internal sdk tools)"
  go vet ./...
  go test -timeout 10m ./...
fi

step "Build"
make release VERSION="$V" RELEASE_IMAGES="$IMAGES"
(cd dist && sha256sum -c --quiet SHA256SUMS) || die "dist/SHA256SUMS does not match"
[ "$(./dist/synctl-linux-amd64 version | awk '{print $2}')" = "$V" ] || die "the binaries do not report $V"

# Release notes: the CHANGELOG section, then how to install.
notes=$(mktemp)
awk -v v="$V" 'index($0, "## [" v "]") == 1 {on=1; next} on && /^## \[/ {exit} on' CHANGELOG.md > "$notes"
cat >> "$notes" <<EOF

## Install

\`\`\`sh
curl -fsSL https://github.com/$REPO/releases/download/$TAG/install.sh | sudo bash -s -- --version $V
\`\`\`

Or download \`syncloud_${V}_linux_<arch>.tar.gz\`, unpack it and run \`sudo ./install.sh --from-dir .\`.
Existing clusters upgrade from **Settings → Updates**, or with \`sudo syncloud-controller upgrade --version $V\`.
Every file is listed in \`SHA256SUMS\`.
EOF

if [ "$DRY" = 1 ]; then
  step "Dry run: dist/ is ready; not tagging or publishing"
  cat "$notes"; rm -f "$notes"; exit 0
fi

step "Tag and publish"
pre=""; case "$V" in *-*) pre=--prerelease ;; esac
git tag -a "$TAG" -m "SynCloud $TAG"
git push origin "$TAG"
gh release create "$TAG" -R "$REPO" --verify-tag --title "SynCloud $TAG" --notes-file "$notes" $pre $DRAFT dist/*
rm -f "$notes"
echo
echo "Published https://github.com/$REPO/releases/tag/$TAG"
