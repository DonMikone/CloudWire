#!/usr/bin/env bash
# Package build/CloudWire.app into build/CloudWire-$VERSION.dmg plus a .sha256 file.
# Usage: VERSION=0.1.0 scripts/make-dmg.sh   (run `make app` first)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BUILD="$ROOT/build"
APP="$BUILD/CloudWire.app"
STAGE="$BUILD/dmg"

if [ -z "${VERSION:-}" ]; then
  VERSION="$(git -C "$ROOT" describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' || true)"
  VERSION="${VERSION:-0.1.0}"
fi
DMG_NAME="CloudWire-$VERSION.dmg"
DMG="$BUILD/$DMG_NAME"

if [ ! -d "$APP" ]; then
  echo "missing $APP; run 'make app' first" >&2
  exit 1
fi

echo "==> staging $STAGE"
rm -rf "$STAGE"
mkdir -p "$STAGE"
ditto "$APP" "$STAGE/CloudWire.app"
ln -s /Applications "$STAGE/Applications"

echo "==> creating $DMG"
rm -f "$DMG" "$DMG.sha256"
hdiutil create -volname CloudWire -srcfolder "$STAGE" -ov -format ULFO "$DMG"

# The checksum file names the DMG without a path so `shasum -c` works next to the download.
(cd "$BUILD" && shasum -a 256 "$DMG_NAME" > "$DMG_NAME.sha256")
rm -rf "$STAGE"

echo "==> $(cat "$DMG.sha256")"
