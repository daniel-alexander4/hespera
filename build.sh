#!/usr/bin/env bash
# Cross-compile Hespera for all platforms and build Linux .deb packages.
# Usage: ./build.sh [version]   (version defaults to the VERSION file)
#
# Produces one cgo-free static `hespera` binary per OS/arch in dist/ (the
# server — the assets are embedded, so each binary is fully self-contained),
# plus a .deb for linux amd64/arm64 when nfpm is installed
# (go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest). The .deb also
# carries the `hescli` admin stub and declares ffmpeg as a dependency so apt
# pulls it.
set -euo pipefail
cd "$(dirname "$0")"

VERSION="${1:-$(cat VERSION 2>/dev/null || echo dev)}"
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
