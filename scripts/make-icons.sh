#!/usr/bin/env bash
# Render the CloudWire logo SVGs into the app asset catalog and the docs.
# Requires rsvg-convert (brew install librsvg). The generated files are committed,
# so CI and normal builds never need to run this script.
# Usage: scripts/make-icons.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LOGO="$ROOT/assets/logo"
ASSETS="$ROOT/app/CloudWire/Assets.xcassets"
APPICON="$ASSETS/AppIcon.appiconset"
MENUBAR="$ASSETS/MenuBarIcon.imageset"
DOCS_IMAGES="$ROOT/docs/images"

command -v rsvg-convert >/dev/null 2>&1 || {
  echo "rsvg-convert not found; install it with: brew install librsvg" >&2
  exit 1
}

render() { # <svg> <pixels> <output>
  rsvg-convert --width "$2" --height "$2" --keep-aspect-ratio --format png "$1" --output "$3"
}

mkdir -p "$APPICON" "$MENUBAR" "$DOCS_IMAGES"

# App icon: every macOS slot, 16...512 pt at @1x and @2x.
rm -f "$APPICON"/*.png
images=""
for pt in 16 32 128 256 512; do
  for scale in 1 2; do
    px=$((pt * scale))
    if [ "$scale" -eq 1 ]; then name="icon_${pt}x${pt}.png"; else name="icon_${pt}x${pt}@2x.png"; fi
    render "$LOGO/cloudwire-icon.svg" "$px" "$APPICON/$name"
    images="$images${images:+,}
    { \"filename\" : \"$name\", \"idiom\" : \"mac\", \"scale\" : \"${scale}x\", \"size\" : \"${pt}x${pt}\" }"
  done
done
cat > "$APPICON/Contents.json" <<EOF
{
  "images" : [$images
  ],
  "info" : { "author" : "xcode", "version" : 1 }
}
EOF

# Menu bar icon: vector template image.
rm -f "$MENUBAR"/*.svg "$MENUBAR"/*.pdf "$MENUBAR"/*.png
cp "$LOGO/cloudwire-menubar.svg" "$MENUBAR/MenuBarIcon.svg"
cat > "$MENUBAR/Contents.json" <<'EOF'
{
  "images" : [
    { "filename" : "MenuBarIcon.svg", "idiom" : "universal" }
  ],
  "info" : { "author" : "xcode", "version" : 1 },
  "properties" : {
    "preserves-vector-representation" : true,
    "template-rendering-intent" : "template"
  }
}
EOF

# README / docs logo.
render "$LOGO/cloudwire-icon.svg" 256 "$DOCS_IMAGES/logo-256.png"

echo "==> icons written to $APPICON, $MENUBAR and $DOCS_IMAGES/logo-256.png"
