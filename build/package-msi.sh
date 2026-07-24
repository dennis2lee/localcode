#!/usr/bin/env bash
# Builds a Windows .msi installer for the amd64 (x64) build using msitools'
# `wixl` — this runs entirely on macOS/Linux, no Windows or real WiX Toolset
# needed (`brew install msitools`).
#
# arm64 is intentionally not covered here: this version of wixl rejects
# `-a arm64` ("arch of type 'arm64' is not supported"). arm64 users get the
# portable zip from package-windows.sh instead.
#
# The MSI also bundles localcode-gui.exe and a Start Menu shortcut for it.
# That binary is CGo (a native webview) and cannot be cross-compiled from
# macOS, so it is NOT built by this script — it has to already exist,
# built on a real Windows machine or downloaded from the
# .github/workflows/gui-windows.yml CI artifact. Pass its path as $3.
#
# NOTE: the .msi is unsigned. SmartScreen/Defender will warn on first run
# until it's signed with a code-signing certificate (`signtool sign` on a
# real Windows box, or osslsigncode cross-platform) — add that as a
# follow-up once you have a certificate.
set -euo pipefail

VERSION="${1:-0.1.0}"
DIST="${2:-dist}"
GUI_EXE_PATH="${3:-}"
BIN_NAME="localcode"
LDFLAGS="-s -w -X main.version=${VERSION}"
WEBVIEW2_BOOTSTRAPPER_URL="https://go.microsoft.com/fwlink/p/?LinkId=2124703"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if ! command -v wixl >/dev/null 2>&1; then
	echo "error: wixl not found. Install with: brew install msitools" >&2
	exit 1
fi

if [ -z "$GUI_EXE_PATH" ] || [ ! -f "$GUI_EXE_PATH" ]; then
	echo "error: need a path to a built localcode-gui.exe as \$3" >&2
	echo "  it is CGo and cannot be cross-compiled here; build it on Windows or" >&2
	echo "  download it: gh run download <run-id> -n localcode-gui-windows-amd64" >&2
	exit 1
fi

OUT="$DIST/windows"
mkdir -p "$OUT"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "==> building windows/amd64"
GOOS=windows GOARCH=amd64 go build -ldflags "$LDFLAGS" -o "$WORK/${BIN_NAME}.exe" ./cmd/localcode

echo "==> fetching WebView2 Evergreen Bootstrapper"
curl -fsSL -o "$WORK/MicrosoftEdgeWebview2Setup.exe" "$WEBVIEW2_BOOTSTRAPPER_URL"

MSI="$OUT/${BIN_NAME}-${VERSION}-windows-amd64.msi"
echo "==> wixl -> $MSI"
wixl -a x64 \
	-D "Version=${VERSION}" \
	-D "ExePath=$WORK/${BIN_NAME}.exe" \
	-D "GuiExePath=$GUI_EXE_PATH" \
	-D "WebView2BootstrapperPath=$WORK/MicrosoftEdgeWebview2Setup.exe" \
	-o "$MSI" \
	build/localcode.wxs

echo "==> done: $MSI"
ls -la "$MSI"
