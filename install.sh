#!/usr/bin/env bash
# Install the Hespera .deb. Build the package(s) first with ./build.sh (which
# produces the per-arch .debs in dist/).
#
# Usage:
#   ./install.sh [version]        install locally (this machine's architecture);
#                                 installs hespera + the hesplay client .deb
#   ./install.sh remote [host]    copy the amd64 hespera .deb to host (default:
#                                 plex) and install it there with sudo over ssh
#   ./install.sh client [host]    copy JUST the hesplay client .deb to host
#                                 (default: raspberrypi; arch asked of the box)
#                                 and install it — no full Hespera needed
set -euo pipefail
cd "$(dirname "$0")"

# --- Client deploy: push only the hesplay .deb to a box with speakers ----------
if [ "${1:-}" = "client" ]; then
  HOST="${2:-raspberrypi}"
  VERSION="$(cat VERSION 2>/dev/null || echo 0.0.0)"
  ARCH="$(ssh "$HOST" dpkg --print-architecture)"
  DEB="dist/hesplay_${VERSION}_${ARCH}.deb"
  if [ ! -f "$DEB" ]; then
    echo "$DEB not found — run ./build.sh first" >&2
    exit 1
  fi
  REMOTE_DEB="/tmp/$(basename "$DEB")"
  echo "deploying hesplay $VERSION ($ARCH) to $HOST…"
  scp "$DEB" "$HOST:$REMOTE_DEB"
  # apt-get resolves the mpv|ffmpeg engine dependency; the file is removed after.
  ssh -t "$HOST" "sudo apt-get install -y -qq -o APT::Sandbox::User=root '$REMOTE_DEB'; rm -f '$REMOTE_DEB'"
  echo "installed on $HOST — try: hesplay server http://plex:8080"
  exit 0
fi

# --- Remote deploy: push the amd64 .deb to a server and apt-install it ---------
if [ "${1:-}" = "remote" ]; then
  HOST="${2:-plex}"
  VERSION="$(cat VERSION 2>/dev/null || echo 0.0.0)"
  DEB="dist/hespera_${VERSION}_amd64.deb" # servers are amd64
  if [ ! -f "$DEB" ]; then
    echo "$DEB not found — run ./build.sh first" >&2
    exit 1
  fi
  REMOTE_DEB="/tmp/$(basename "$DEB")"
  echo "deploying hespera $VERSION (amd64) to $HOST…"
  scp "$DEB" "$HOST:$REMOTE_DEB"
  # -t: allocate a tty so sudo can prompt for a password if it needs one.
  # apt-get (not dpkg -i) resolves the ffmpeg dependency; the file is removed after.
  ssh -t "$HOST" "sudo apt-get install -y -qq -o APT::Sandbox::User=root '$REMOTE_DEB'; rm -f '$REMOTE_DEB'"
  echo "installed on $HOST — restart the running hespera there to pick up the new binary."
  exit 0
fi

# --- Local install ------------------------------------------------------------
VERSION="${1:-$(cat VERSION 2>/dev/null || echo 0.0.0)}"
ARCH="$(dpkg --print-architecture)" # amd64 or arm64
DEB="dist/hespera_${VERSION}_${ARCH}.deb"

if [ ! -f "$DEB" ]; then
  echo "$DEB not found — run ./build.sh first" >&2
  exit 1
fi

echo "installing hespera $VERSION ($ARCH)…"
# apt-get (not dpkg -i) so the ffmpeg dep resolves; -qq keeps it quiet;
# APT::Sandbox::User=root lets apt read the .deb from your home dir without the
# "_apt couldn't access" permission warning. The hesplay client rides along in
# its own .deb (since 0.39.4 the hespera package no longer ships it) — install
# both in ONE transaction: hesplay takes over /usr/bin/hesplay from the old
# hespera package via Replaces, which apt resolves cleanly only when it sees
# both packages together.
PLAY_DEB="dist/hesplay_${VERSION}_${ARCH}.deb"
if [ -f "$PLAY_DEB" ]; then
  sudo apt-get install -y -qq -o APT::Sandbox::User=root "./$DEB" "./$PLAY_DEB"
else
  echo "note: $PLAY_DEB not found — installing hespera only (hesplay is its own .deb now)" >&2
  sudo apt-get install -y -qq -o APT::Sandbox::User=root "./$DEB"
fi

# Stop any instance still running the OLD binary. The app's attach-first launch
# matches a live instance by its recorded app.url and opens a window onto THAT —
# so a lingering old process means the next launch shows the old version and the
# update looks like it "didn't take". SIGTERM is the app's clean-shutdown path
# (it clears app.url); the KILL backstop covers a hung one. Matched by exact
# process name, so hescli (a different binary) is never touched; runs as you,
# so it only signals your own processes.
if pgrep -x hespera >/dev/null 2>&1; then
  echo "stopping the running hespera so the next launch starts $VERSION…"
  pkill -TERM -x hespera 2>/dev/null || true
  for _ in {1..20}; do pgrep -x hespera >/dev/null 2>&1 || break; sleep 0.25; done
  pkill -KILL -x hespera 2>/dev/null || true
fi

# Refresh the desktop + icon caches so the menu entry and icon appear now.
sudo update-desktop-database >/dev/null 2>&1 || true
sudo gtk-update-icon-cache /usr/share/icons/hicolor >/dev/null 2>&1 || true

echo "done — relaunch Hespera to start $VERSION."
