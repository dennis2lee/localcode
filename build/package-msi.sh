#!/usr/bin/env bash
# Builds a Windows .msi installer for the amd64 (x64) build using msitools'
# `wixl` — this runs entirely on macOS/Linux, no Windows or real WiX Toolset
# needed (`brew install msitools`).
#
# arm64 is intentionally not covered here: this version of wixl rejects
# `-a arm64` ("arch of type 'arm64' is not supported"). arm64 users get the
# portable zip from package-windows.sh instead.
#
# NOTE: the .msi is unsigned. SmartScreen/Defender will warn on first run
# until it's signed with a code-signing certificate (`signtool sign` on a
# real Windows box, or osslsigncode cross-platform) — add that as a
# follow-up once you have a certificate.
set -euo pipefail

VERSION="${1:-0.1.0}"
DIST="${2:-dist}"
BIN_NAME="localcode"
LDFLAGS="-s -w -X main.version=${VERSION}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if ! command -v wixl >/dev/null 2>&1; then
	echo "error: wixl not found. Install with: brew install msitools" >&2
	exit 1
fi

OUT="$DIST/windows"
mkdir -p "$OUT"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "==> building windows/amd64"
GOOS=windows GOARCH=amd64 go build -ldflags "$LDFLAGS" -o "$WORK/${BIN_NAME}.exe" ./cmd/localcode

MSI="$OUT/${BIN_NAME}-${VERSION}-windows-amd64.msi"
echo "==> wixl -> $MSI"
wixl -a x64 \
	-D "Version=${VERSION}" \
	-D "ExePath=$WORK/${BIN_NAME}.exe" \
	-o "$MSI" \
	build/localcode.wxs

echo "==> done: $MSI"
ls -la "$MSI"
