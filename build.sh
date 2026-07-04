#!/usr/bin/env bash
# Cross-compile Hespera for all platforms and build Linux .deb packages.
# Usage: ./build.sh [-p|--publish] [version]   (version defaults to the VERSION file)
#
# Produces one cgo-free static `hespera` binary per OS/arch in dist/ (the
# server — the assets are embedded, so each binary is fully self-contained),
# plus a .deb for linux amd64/arm64 when nfpm is installed
# (go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest). The .deb also
# carries the `hescli` admin stub and declares ffmpeg as a dependency so apt
# pulls it.
#
# -p / --publish: after building, push main and publish the dist/ artifacts as
# GitHub release v<version> (the release channel the in-app update pill
# checks). Requires gh (authenticated), nfpm (a release without the .debs
# would strand deb installs' update downloads), and a clean, committed tree.
set -euo pipefail
cd "$(dirname "$0")"

PUBLISH=0
ARGS=()
for a in "$@"; do
  case "$a" in
    -p|--publish) PUBLISH=1 ;;
    -h|--help) sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*) echo "unknown flag: $a (usage: ./build.sh [-p|--publish] [version])" >&2; exit 1 ;;
    *) ARGS+=("$a") ;;
  esac
done
VERSION="${ARGS[0]:-$(cat VERSION 2>/dev/null || echo dev)}"

if [ "$PUBLISH" = 1 ]; then
  # Fail the preconditions before spending minutes building.
  command -v gh >/dev/null 2>&1 || { echo "publish needs the gh CLI (authenticated)" >&2; exit 1; }
  command -v nfpm >/dev/null 2>&1 || { echo "publish needs nfpm — a release without the .debs would strand deb installs (go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest)" >&2; exit 1; }
  [ "$VERSION" != "dev" ] || { echo "publish needs a real version (VERSION file missing?)" >&2; exit 1; }
  git diff-index --quiet HEAD -- || { echo "publish needs a clean tree — commit first (the release tag must match the built code)" >&2; exit 1; }
  if gh release view "v$VERSION" >/dev/null 2>&1; then
    echo "release v$VERSION already exists — bump VERSION (./bump.sh patch) and commit first" >&2
    exit 1
  fi
fi

DIST="dist"
rm -rf "$DIST"
mkdir -p "$DIST"

LDFLAGS="-s -w -X main.version=$VERSION"

targets=(
  "linux/amd64" "linux/arm64"
  "darwin/amd64" "darwin/arm64"
  "windows/amd64" "windows/arm64"
)

for t in "${targets[@]}"; do
  os="${t%/*}"; arch="${t#*/}"
  ext=""; [ "$os" = "windows" ] && ext=".exe"
  out="$DIST/hespera-$VERSION-$os-$arch$ext"
  echo "building $out"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "$LDFLAGS" -o "$out" ./cmd/hespera
done

if command -v nfpm >/dev/null 2>&1; then
  for arch in amd64 arm64; do
    echo "packaging hespera_${VERSION}_${arch}.deb"
    # nfpm.yaml references the literal staged paths dist/hespera and dist/hescli.
    cp "$DIST/hespera-$VERSION-linux-$arch" "$DIST/hespera"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
      -ldflags "$LDFLAGS" -o "$DIST/hescli" ./cmd/hescli
    ARCH="$arch" VERSION="$VERSION" \
      nfpm package --config build/nfpm.yaml --packager deb --target "$DIST/hespera_${VERSION}_${arch}.deb"
  done
  rm -f "$DIST/hespera" "$DIST/hescli"
else
  echo "nfpm not found — skipping .deb. Install: go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest"
fi

echo "done — artifacts in $DIST/"

if [ "$PUBLISH" = 1 ]; then
  echo "pushing main and publishing release v$VERSION"
  git push origin main
  # The asset names are load-bearing: the in-app update pill's asset picker
  # matches hespera_<ver>_<arch>.deb and hespera-<ver>-<os>-<arch> exactly.
  gh release create "v$VERSION" \
    --title "Hespera $VERSION" \
    --generate-notes \
    "$DIST"/*
  echo "published — https://github.com/daniel-alexander4/hespera/releases/tag/v$VERSION"
fi
