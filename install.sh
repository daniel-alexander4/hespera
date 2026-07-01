#!/usr/bin/env bash
# Install the Hespera .deb for this machine. Build the package first with
# ./build.sh (which produces the per-arch .debs in dist/).
# Usage: ./install.sh [version]   (version defaults to the VERSION file)
set -euo pipefail
cd "$(dirname "$0")"

VERSION="${1:-$(cat VERSION 2>/dev/null || echo 0.0.0)}"
ARCH="$(dpkg --print-architecture)" # amd64 or arm64
DEB="dist/hespera_${VERSION}_${ARCH}.deb"

if [ ! -f "$DEB" ]; then
  echo "$DEB not found — run ./build.sh first" >&2
  exit 1
fi

echo "installing hespera $VERSION ($ARCH)…"
# apt-get (not dpkg -i) so the ffmpeg/openssh-client deps resolve; -qq keeps it
# quiet; APT::Sandbox::User=root lets apt read the .deb from your home dir without
# the "_apt couldn't access" permission warning.
sudo apt-get install -y -qq -o APT::Sandbox::User=root "./$DEB"

# Refresh the desktop + icon caches so the menu entry and icon appear now.
sudo update-desktop-database >/dev/null 2>&1 || true
sudo gtk-update-icon-cache /usr/share/icons/hicolor >/dev/null 2>&1 || true

echo "installed — launch 'Hespera' from the app menu or run 'hespera'."
