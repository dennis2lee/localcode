#!/usr/bin/env bash
# Cross-compiles darwin/amd64 + darwin/arm64, lipo's them into a universal
# binary, wraps it in a minimal unsigned .app bundle, and tars both the
# raw binary and the .app for distribution.
#
# NOTE: this produces an *unsigned* .app. macOS Gatekeeper will refuse to
# run it without the user right-clicking -> Open the first time (or the
# app being code-signed + notarized with an Apple Developer ID, which
# needs `codesign`/`xcrun notarytool` and real Apple credentials this
# script does not have — add that as a separate signing step once you
# have a Developer ID).
set -euo pipefail

VERSION="${1:-0.1.0}"
DIST="${2:-dist}"
APP_NAME="LocalCode"
BIN_NAME="localcode"
LDFLAGS="-s -w -X main.version=${VERSION}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

OUT="$DIST/mac"
rm -rf "$OUT"
mkdir -p "$OUT"

echo "==> building darwin/amd64"
GOOS=darwin GOARCH=amd64 go build -ldflags "$LDFLAGS" -o "$OUT/${BIN_NAME}-amd64" ./cmd/localcode

echo "==> building darwin/arm64"
GOOS=darwin GOARCH=arm64 go build -ldflags "$LDFLAGS" -o "$OUT/${BIN_NAME}-arm64" ./cmd/localcode

echo "==> lipo -> universal binary"
lipo -create -output "$OUT/${BIN_NAME}" "$OUT/${BIN_NAME}-amd64" "$OUT/${BIN_NAME}-arm64"
rm "$OUT/${BIN_NAME}-amd64" "$OUT/${BIN_NAME}-arm64"
chmod +x "$OUT/${BIN_NAME}"

echo "==> packaging raw binary tar.gz"
tar -C "$OUT" -czf "$OUT/${BIN_NAME}-${VERSION}-darwin-universal.tar.gz" "${BIN_NAME}"

echo "==> building ${APP_NAME}.app bundle"
APP="$OUT/${APP_NAME}.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS"
cp "$OUT/${BIN_NAME}" "$APP/Contents/MacOS/${BIN_NAME}-bin"

# This is a TUI app: launching the raw binary via Finder attaches no
# terminal, so the bundle's entry point opens Terminal.app and runs the
# real binary inside it instead of executing it directly.
cat > "$APP/Contents/MacOS/${BIN_NAME}" <<'LAUNCHER'
#!/bin/bash
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
open -a Terminal "$DIR/localcode-bin"
LAUNCHER
chmod +x "$APP/Contents/MacOS/${BIN_NAME}"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key>
	<string>${APP_NAME}</string>
	<key>CFBundleIdentifier</key>
	<string>dev.localcode.app</string>
	<key>CFBundleVersion</key>
	<string>${VERSION}</string>
	<key>CFBundleShortVersionString</key>
	<string>${VERSION}</string>
	<key>CFBundleExecutable</key>
	<string>${BIN_NAME}</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>LSMinimumSystemVersion</key>
	<string>11.0</string>
	<key>NSHighResolutionCapable</key>
	<true/>
</dict>
</plist>
PLIST

tar -C "$OUT" -czf "$OUT/${APP_NAME}-${VERSION}-darwin-universal-app.tar.gz" "${APP_NAME}.app"

echo "==> done: $OUT"
ls -la "$OUT"
