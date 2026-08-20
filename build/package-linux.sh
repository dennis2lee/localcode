#!/usr/bin/env bash
# Cross-compiles linux/amd64 and linux/arm64 and packages each two ways: a
# portable tar.gz, and a .deb for Debian and Ubuntu.
#
# CGO_ENABLED=0 is the point of the whole thing. The daemon, the TUI and
# every tool are pure Go, so a static binary runs on any glibc and any
# musl, from Ubuntu 24.04 back to whatever the machine on the other side
# of the room is running — and the .deb needs no Depends line at all. The
# native desktop window is the exception (it links a webview through CGo)
# and is not part of this: on Linux, localcode is the daemon, the TUI and
# the Web UI in a browser.
set -euo pipefail

VERSION="${1:-0.1.0}"
DIST="${2:-dist}"
BIN_NAME="localcode"
LDFLAGS="-s -w -X main.version=${VERSION}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

OUT="$DIST/linux"
mkdir -p "$OUT"
rm -f "$OUT"/*.tar.gz "$OUT"/*.deb

for ARCH in amd64 arm64; do
	echo "==> building linux/${ARCH}"
	CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -ldflags "$LDFLAGS" -o "$OUT/${BIN_NAME}" ./cmd/localcode

	TGZ="$OUT/${BIN_NAME}-${VERSION}-linux-${ARCH}.tar.gz"
	echo "==> packaging $TGZ"
	tar -C "$OUT" -czf "$TGZ" "${BIN_NAME}"

	DEB="$OUT/${BIN_NAME}-${VERSION}-linux-${ARCH}.deb"
	echo "==> packaging $DEB"
	go run ./build/deb -version "$VERSION" -arch "$ARCH" -bin "$OUT/${BIN_NAME}" -out "$DEB"

	rm "$OUT/${BIN_NAME}"
done

echo "==> done: $OUT"
ls -la "$OUT"
