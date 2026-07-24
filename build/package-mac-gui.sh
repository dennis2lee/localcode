#!/usr/bin/env bash
# Builds the native-window (webview) build of localcode into a
# double-clickable LocalCode.app for macOS.
#
# Unlike package-mac.sh (the pure-Go TUI app, cross-compiled with GOOS),
# this links a native WKWebView through CGo, so it CANNOT be cross-compiled
# from another OS and each arch is built on this Mac: arm64 natively, amd64
# via clang's -arch x86_64. The two are lipo'd into a universal binary.
#
# The result is UNSIGNED. Gatekeeper will require a right-click -> Open the
# first time until it is code-signed + notarized with an Apple Developer ID.
set -euo pipefail

VERSION="${1:-0.1.0}"
DIST="${2:-dist}"
APP_NAME="LocalCode"
BIN_NAME="localcode"
LDFLAGS="-s -w -X main.version=${VERSION}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [ "$(uname)" != "Darwin" ]; then
	echo "package-mac-gui.sh must run on macOS (CGo webview cannot be cross-compiled)" >&2
	exit 1
fi

OUT="$DIST/mac-gui"
rm -rf "$OUT"
mkdir -p "$OUT"

echo "==> building darwin/arm64 (native, -tags gui)"
CGO_ENABLED=1 GOARCH=arm64 go build -tags gui -ldflags "$LDFLAGS" -o "$OUT/${BIN_NAME}-arm64" ./cmd/localcode

echo "==> building darwin/amd64 (clang -arch x86_64, -tags gui)"
CGO_ENABLED=1 GOARCH=amd64 CGO_CFLAGS="-arch x86_64" CGO_LDFLAGS="-arch x86_64" \
	go build -tags gui -ldflags "$LDFLAGS" -o "$OUT/${BIN_NAME}-amd64" ./cmd/localcode

echo "==> lipo -> universal binary"
lipo -create -output "$OUT/${BIN_NAME}-bin" "$OUT/${BIN_NAME}-amd64" "$OUT/${BIN_NAME}-arm64"
rm "$OUT/${BIN_NAME}-amd64" "$OUT/${BIN_NAME}-arm64"
chmod +x "$OUT/${BIN_NAME}-bin"

echo "==> building ${APP_NAME}.app bundle"
APP="$OUT/${APP_NAME}.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS"
cp "$OUT/${BIN_NAME}-bin" "$APP/Contents/MacOS/${BIN_NAME}-bin"
rm "$OUT/${BIN_NAME}-bin"

# Entry point: run the real binary with --gui so Finder launch opens the
# native window directly (no terminal). exec keeps it as the app's process
# so the Dock/window associate with the bundle.
cat > "$APP/Contents/MacOS/${APP_NAME}" <<'LAUNCHER'
#!/bin/bash
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$DIR/localcode-bin" --gui
LAUNCHER
chmod +x "$APP/Contents/MacOS/${APP_NAME}"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key>
	<string>${APP_NAME}</string>
	<key>CFBundleIdentifier</key>
	<string>dev.localcode.gui</string>
	<key>CFBundleVersion</key>
	<string>${VERSION}</string>
	<key>CFBundleShortVersionString</key>
	<string>${VERSION}</string>
	<key>CFBundleExecutable</key>
	<string>${APP_NAME}</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>LSMinimumSystemVersion</key>
	<string>11.0</string>
	<key>NSHighResolutionCapable</key>
	<true/>
</dict>
</plist>
PLIST

tar -C "$OUT" -czf "$OUT/${APP_NAME}-${VERSION}-darwin-universal-gui-app.tar.gz" "${APP_NAME}.app"

echo "==> done: $OUT"
ls -la "$OUT"
