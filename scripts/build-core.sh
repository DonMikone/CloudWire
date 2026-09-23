#!/usr/bin/env bash
# Build the CloudWire Core (Go + embedded rclone).
# Usage: scripts/build-core.sh <debug|release> [native|universal]
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MODE="${1:-release}"
ARCHES="${2:-universal}"
VERSION="${VERSION:-0.0.0-dev}"
OUT="$ROOT/build/core"
FUSE_INCLUDE="$ROOT/core/third_party/fuse/include"
# Link the system SQLite (/usr/lib/libsqlite3.dylib). go-sqlite3's libsqlite3 tag
# hardcodes Homebrew search paths; the SDK paths given first take precedence,
# and the SDK header has no load-extension API.
SDK="$(xcrun --sdk macosx --show-sdk-path)"
TAGS="cmount libsqlite3 sqlite_omit_load_extension"

RCLONE_VERSION="$(cd "$ROOT/core" && go list -m -f '{{.Version}}' github.com/rclone/rclone)"
XFLAGS="-X github.com/DonMikone/CloudWire/core/internal/buildinfo.Version=$VERSION -X github.com/rclone/rclone/fs.Version=$RCLONE_VERSION"
case "$MODE" in
  debug) LDFLAGS="$XFLAGS" ;;
  release) LDFLAGS="-s -w $XFLAGS" ;;
  *) echo "usage: $0 <debug|release> [native|universal]" >&2; exit 2 ;;
esac

mkdir -p "$OUT"

build_arch() {
  local goarch="$1" clangarch="$2" dest="$3" out
  echo "==> cloudwire-core $VERSION ($MODE, $goarch)"
  rm -f "$dest" # go build refuses to overwrite a universal (fat) binary
  if ! out="$(cd "$ROOT/core" && \
    CGO_ENABLED=1 GOOS=darwin GOARCH="$goarch" \
    CC="clang -arch $clangarch -mmacosx-version-min=14.0" \
    CGO_CFLAGS="-mmacosx-version-min=14.0 -I$SDK/usr/include -I$FUSE_INCLUDE" \
    CGO_LDFLAGS="-mmacosx-version-min=14.0 -L$SDK/usr/lib" \
    go build -tags "$TAGS" -trimpath -ldflags "$LDFLAGS" -o "$dest" ./cmd/cloudwire-core 2>&1)"; then
    echo "$out" >&2
    exit 1
  fi
  # go-sqlite3 adds Homebrew search paths that may not exist; that warning is noise.
  echo "$out" | grep -v "search path '/usr/local/opt/sqlite/lib' not found" | grep . >&2 || true
  if otool -L "$dest" | grep -q 'opt/sqlite'; then
    echo "error: $dest links a Homebrew SQLite instead of /usr/lib/libsqlite3.dylib" >&2
    exit 1
  fi
}

case "$ARCHES" in
  native)
    case "$(uname -m)" in
      arm64) build_arch arm64 arm64 "$OUT/cloudwire-core" ;;
      x86_64) build_arch amd64 x86_64 "$OUT/cloudwire-core" ;;
      *) echo "unsupported host arch $(uname -m)" >&2; exit 1 ;;
    esac
    ;;
  universal)
    build_arch arm64 arm64 "$OUT/cloudwire-core-arm64"
    build_arch amd64 x86_64 "$OUT/cloudwire-core-amd64"
    lipo -create -output "$OUT/cloudwire-core" "$OUT/cloudwire-core-arm64" "$OUT/cloudwire-core-amd64"
    rm -f "$OUT/cloudwire-core-arm64" "$OUT/cloudwire-core-amd64"
    ;;
  *) echo "usage: $0 <debug|release> [native|universal]" >&2; exit 2 ;;
esac

echo "==> $(lipo -archs "$OUT/cloudwire-core") $OUT/cloudwire-core"
