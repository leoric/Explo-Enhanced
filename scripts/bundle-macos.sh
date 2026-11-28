#!/usr/bin/env bash
# Builds Explo.app (ad-hoc signed) from the current checkout. Run on a Mac.
#   scripts/bundle-macos.sh                # native architecture
#   UNIVERSAL=1 scripts/bundle-macos.sh    # Apple Silicon + Intel (needs both rustup targets)
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION=$(cargo metadata --no-deps --format-version 1 | python3 -c 'import json,sys; print(next(p["version"] for p in json.load(sys.stdin)["packages"] if p["name"]=="explo"))')
APP="target/Explo.app"

if [[ "${UNIVERSAL:-0}" == "1" ]]; then
  rustup target add aarch64-apple-darwin x86_64-apple-darwin
  cargo build --release -p explo --target aarch64-apple-darwin
  cargo build --release -p explo --target x86_64-apple-darwin
  BIN=target/explo-universal
  lipo -create -output "$BIN" target/aarch64-apple-darwin/release/explo target/x86_64-apple-darwin/release/explo
else
  cargo build --release -p explo
  BIN=target/release/explo
fi

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$BIN" "$APP/Contents/MacOS/explo"
[[ -f assets/Explo.icns ]] && cp assets/Explo.icns "$APP/Contents/Resources/Explo.icns"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>Explo</string>
  <key>CFBundleDisplayName</key><string>Explo</string>
  <key>CFBundleIdentifier</key><string>com.explo.app</string>
  <key>CFBundleExecutable</key><string>explo</string>
  <key>CFBundleIconFile</key><string>Explo</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>${VERSION}</string>
  <key>CFBundleVersion</key><string>${VERSION}</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
PLIST

codesign --force --deep --sign - "$APP"
echo
echo "Built $APP"
echo "Move it to /Applications BEFORE installing the weekly schedule (the schedule remembers this path)."
echo "First launch: right-click > Open, since it is not notarized."
