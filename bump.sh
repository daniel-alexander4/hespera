#!/usr/bin/env bash
# Bump Hespera's semantic version in the VERSION file (X.Y.Z).
# Usage: ./bump.sh [patch|minor|major]   (default: patch)
#
#   patch (Z) — a minor change or fix     (X.Y.Z   -> X.Y.Z+1)
#   minor (Y) — a major feature           (X.Y.Z   -> X.Y+1.0)
#   major (X) — a breaking/major release  (X.Y.Z   -> X+1.0.0)
#
# build.sh / install.sh read VERSION and stamp it into the binary via
# -ldflags "-X main.version=$VERSION", so a bump takes effect on the next build.
set -euo pipefail
cd "$(dirname "$0")"

part="${1:-patch}"
cur="$(cat VERSION 2>/dev/null || echo 0.0.0)"
if [[ ! "$cur" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "VERSION is not X.Y.Z: '$cur'" >&2
  exit 1
fi
IFS=. read -r x y z <<<"$cur"

case "$part" in
  major) x=$((x + 1)); y=0; z=0 ;;
  minor) y=$((y + 1)); z=0 ;;
  patch) z=$((z + 1)) ;;
  *) echo "usage: $(basename "$0") [patch|minor|major]" >&2; exit 1 ;;
esac

new="$x.$y.$z"
printf '%s\n' "$new" >VERSION
echo "VERSION: $cur -> $new"
