#!/usr/bin/env bash
# Build Hespera for this machine, package it as a .deb, and install it.
# Usage: ./install.sh [version]   (version defaults to the VERSION file)
#
# Installs the `hespera` server + `hescli` to /usr/bin and lets apt pull the
# runtime dependencies (ffmpeg, openssh-client). No service is installed — start
# the server yourself (`hespera`), point HESPERA_MEDIA_ROOT at your media, and
# browse to http://localhost:8080.
set -euo pipefail
cd "$(dirname "$0")"

VERSION="${1:-$(cat VERSION 2>/dev/null || echo 0.0.0)}"
ARCH="$(dpkg --print-architecture)" # amd64 or arm64
DEB="dist/hespera_${VERSION}_${ARCH}.deb"
mkdir -p dist

LDFLAGS="-s -w -X main.version=$VERSION"

echo "building hespera $VERSION ($ARCH)…"
CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o dist/hespera ./cmd/hespera
CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o dist/hescli ./cmd/hescli

if ! command -v nfpm >/dev/null 2>&1; then
  echo "installing nfpm…"
  go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest
  export PATH="$PATH:$(go env GOPATH)/bin"
fi

echo "packaging $DEB…"
ARCH="$ARCH" VERSION="$VERSION" \
  nfpm package --config build/nfpm.yaml --packager deb --target "$DEB"

echo "installing $DEB (sudo)…"
# apt install (not dpkg -i) so the ffmpeg/openssh-client dependencies resolve.
sudo apt install -y "./$DEB"

# Refresh the desktop + icon caches so the menu entry and icon appear now.
sudo update-desktop-database >/dev/null 2>&1 || true
sudo gtk-update-icon-cache /usr/share/icons/hicolor >/dev/null 2>&1 || true

rm -f dist/hespera dist/hescli
echo
echo "Installed. Launch 'Hespera' from your app menu (or run 'hespera') — it opens"
echo "an app window on this machine. Set your media folder in Libraries (or via"
echo "HESPERA_MEDIA_ROOT; default: your home directory)."
