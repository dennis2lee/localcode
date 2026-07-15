#!/usr/bin/env bash
# Cross-compiles windows/amd64 and windows/arm64 .exe binaries and zips each
# for distribution. This is a portable "unzip and run" package, not an
# installer (.msi) — Go cross-compiles trivially, but building a real MSI
# needs WiX or go-msi tooling that isn't set up here. Add that as a
# follow-up once the portable build is validated on real Windows machines.
set -euo pipefail

VERSION="${1:-0.1.0}"
DIST="${2:-dist}"
BIN_NAME="localcode"
LDFLAGS="-s -w -X main.version=${VERSION}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

OUT="$DIST/windows"
rm -rf "$OUT"
mkdir -p "$OUT"

for ARCH in amd64 arm64; do
	echo "==> building windows/${ARCH}"
	GOOS=windows GOARCH="$ARCH" go build -ldflags "$LDFLAGS" -o "$OUT/${BIN_NAME}.exe" ./cmd/localcode

	ZIP="$OUT/${BIN_NAME}-${VERSION}-windows-${ARCH}.zip"
	echo "==> zipping $ZIP"
	(cd "$OUT" && zip -q "$(basename "$ZIP")" "${BIN_NAME}.exe")
	rm "$OUT/${BIN_NAME}.exe"
done

echo "==> done: $OUT"
ls -la "$OUT"
