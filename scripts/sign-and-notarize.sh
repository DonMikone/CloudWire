#!/usr/bin/env bash
# Re-sign build/CloudWire.app with a Developer ID certificate, build the DMG, notarise and staple it.
# Runs only when all Apple secrets are present; otherwise the build stays ad-hoc signed.
#
# Required environment:
#   MACOS_CERT_P12_BASE64  base64 of the "Developer ID Application" certificate + key (.p12)
#   MACOS_CERT_PASSWORD    password of that .p12
#   APPLE_TEAM_ID          10-character team ID of the certificate
#   NOTARY_KEY_ID          App Store Connect API key ID
#   NOTARY_ISSUER_ID       App Store Connect API issuer ID
#   NOTARY_KEY_P8_BASE64   base64 of the AuthKey_<id>.p8 file
# Optional: VERSION (DMG version, defaults like the Makefile).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

for var in MACOS_CERT_P12_BASE64 MACOS_CERT_PASSWORD APPLE_TEAM_ID NOTARY_KEY_ID NOTARY_ISSUER_ID NOTARY_KEY_P8_BASE64; do
  if [ -z "${!var:-}" ]; then
    echo "ad-hoc build"
    exit 0
  fi
done

APP="$ROOT/build/CloudWire.app"
if [ ! -d "$APP" ]; then
  echo "missing $APP; run 'make app' first" >&2
  exit 1
fi
if [ -z "${VERSION:-}" ]; then
  VERSION="$(git -C "$ROOT" describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' || true)"
  VERSION="${VERSION:-0.1.0}"
fi
export VERSION
DMG="$ROOT/build/CloudWire-$VERSION.dmg"

CORE_ENTITLEMENTS="$ROOT/app/CloudWire/Core.entitlements"
FINDER_ENTITLEMENTS="$ROOT/app/CloudWireFinder/CloudWireFinder.entitlements"
APP_ENTITLEMENTS="$ROOT/app/CloudWire/CloudWire.entitlements"

WORK="$(mktemp -d)"
KEYCHAIN="$WORK/cloudwire-signing.keychain-db"
KEYCHAIN_PASSWORD="$(openssl rand -base64 32)"
ORIGINAL_KEYCHAINS=()
while IFS= read -r line; do
  line="${line#"${line%%[![:space:]]*}"}"
  line="${line#\"}"
  line="${line%\"}"
  [ -n "$line" ] && ORIGINAL_KEYCHAINS+=("$line")
done < <(security list-keychains -d user)

cleanup() {
  if [ "${#ORIGINAL_KEYCHAINS[@]}" -gt 0 ]; then
    security list-keychains -d user -s "${ORIGINAL_KEYCHAINS[@]}" || true
  fi
  security delete-keychain "$KEYCHAIN" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "==> importing the signing certificate into a temporary keychain"
printf '%s' "$MACOS_CERT_P12_BASE64" | base64 --decode > "$WORK/cert.p12"
security create-keychain -p "$KEYCHAIN_PASSWORD" "$KEYCHAIN"
security set-keychain-settings -lut 21600 "$KEYCHAIN"
security unlock-keychain -p "$KEYCHAIN_PASSWORD" "$KEYCHAIN"
security import "$WORK/cert.p12" -k "$KEYCHAIN" -P "$MACOS_CERT_PASSWORD" -f pkcs12 -T /usr/bin/codesign
security set-key-partition-list -S apple-tool:,apple:,codesign: -s -k "$KEYCHAIN_PASSWORD" "$KEYCHAIN" >/dev/null
security list-keychains -d user -s "$KEYCHAIN" ${ORIGINAL_KEYCHAINS[@]+"${ORIGINAL_KEYCHAINS[@]}"}
rm -f "$WORK/cert.p12"

IDENTITY="$(security find-identity -v -p codesigning "$KEYCHAIN" \
  | awk -v team="($APPLE_TEAM_ID)" '/Developer ID Application/ && index($0, team) { print $2; exit }')"
if [ -z "$IDENTITY" ]; then
  echo "no 'Developer ID Application' identity for team $APPLE_TEAM_ID in the certificate" >&2
  exit 1
fi

sign() { # <path> [entitlements]
  local args=(--force --options runtime --timestamp --sign "$IDENTITY" --keychain "$KEYCHAIN")
  if [ -n "${2:-}" ]; then args+=(--entitlements "$2"); fi
  codesign "${args[@]}" "$1"
}

# Inside-out; never --deep.
echo "==> signing inside-out with $IDENTITY"
sign "$APP/Contents/MacOS/cloudwire-core" "$CORE_ENTITLEMENTS"
if [ -d "$APP/Contents/Frameworks" ]; then
  find "$APP/Contents/Frameworks" -mindepth 1 -maxdepth 1 \( -name '*.framework' -o -name '*.dylib' \) -print0 \
    | while IFS= read -r -d '' item; do sign "$item"; done
fi
for appex in "$APP"/Contents/PlugIns/*.appex; do
  [ -e "$appex" ] || continue
  sign "$appex" "$FINDER_ENTITLEMENTS"
done
sign "$APP" "$APP_ENTITLEMENTS"
codesign --verify --strict --deep --verbose=2 "$APP"

echo "==> building the DMG"
"$ROOT/scripts/make-dmg.sh"
codesign --force --timestamp --sign "$IDENTITY" --keychain "$KEYCHAIN" "$DMG"

echo "==> notarising $DMG"
printf '%s' "$NOTARY_KEY_P8_BASE64" | base64 --decode > "$WORK/AuthKey_$NOTARY_KEY_ID.p8"
RESULT="$(xcrun notarytool submit "$DMG" \
  --key "$WORK/AuthKey_$NOTARY_KEY_ID.p8" --key-id "$NOTARY_KEY_ID" --issuer "$NOTARY_ISSUER_ID" \
  --wait --output-format json)"
echo "$RESULT"
STATUS="$(printf '%s' "$RESULT" | plutil -extract status raw -o - -)"
if [ "$STATUS" != "Accepted" ]; then
  SUBMISSION="$(printf '%s' "$RESULT" | plutil -extract id raw -o - -)"
  xcrun notarytool log "$SUBMISSION" \
    --key "$WORK/AuthKey_$NOTARY_KEY_ID.p8" --key-id "$NOTARY_KEY_ID" --issuer "$NOTARY_ISSUER_ID" || true
  echo "notarisation failed: $STATUS" >&2
  exit 1
fi

echo "==> stapling"
xcrun stapler staple "$DMG"
# The DMG changed when its ticket was stapled; refresh the checksum.
(cd "$ROOT/build" && shasum -a 256 "$(basename "$DMG")" > "$(basename "$DMG").sha256")
spctl --assess --type open --context context:primary-signature --verbose=2 "$DMG" || true
echo "==> signed and notarised: $DMG"
