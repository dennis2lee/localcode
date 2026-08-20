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
	# Built on a Mac, unpacked on Linux. bsdtar copies macOS extended
	# attributes into pax headers, and GNU tar prints a warning for every
	# one of them on the way out:
	#
	#   tar: Ignoring unknown extended header keyword
	#        'LIBARCHIVE.xattr.com.apple.provenance'
	#
	# Harmless, and it appears in the middle of an install, on stderr,
	# where it reads as the install going wrong. The flags are probed
	# rather than assumed because GNU tar has never heard of
	# --no-mac-metadata and this script also runs on Linux.
	TAR_CLEAN=""
	for flag in --no-mac-metadata --no-xattrs; do
		if tar "$flag" -cf /dev/null -T /dev/null >/dev/null 2>&1; then
			TAR_CLEAN="$TAR_CLEAN $flag"
		fi
	done
	COPYFILE_DISABLE=1 tar $TAR_CLEAN -C "$OUT" -czf "$TGZ" "${BIN_NAME}"

	# And check it worked, on the archive itself. A flag that stops being
	# supported would otherwise put the warning back silently, on a
	# machine that never sees it. The headers sit at the front of the
	# stream, before the binary, so this reads the first block rather than
	# grepping 16MB of Go binary for a word its own syscall table contains.
	# Read into a variable and matched with `case`, not `| grep -q`: this
	# script runs under `set -o pipefail`, grep -q exits at its first
	# match, and the SIGPIPE that sends upstream makes the whole pipeline
	# report failure — so the check would quietly never fire. The same
	# trap is documented in package-msi.sh.
	head="$(gzip -dc "$TGZ" | head -c 4096 | LC_ALL=C tr -d '\000' || true)"
	case "$head" in
	*xattr.com.apple*)
		echo "error: $TGZ carries macOS extended attributes; GNU tar warns on every extraction" >&2
		exit 1
		;;
	esac

	DEB="$OUT/${BIN_NAME}-${VERSION}-linux-${ARCH}.deb"
	echo "==> packaging $DEB"
	go run ./build/deb -version "$VERSION" -arch "$ARCH" -bin "$OUT/${BIN_NAME}" -out "$DEB"

	rm "$OUT/${BIN_NAME}"
done

echo "==> done: $OUT"
ls -la "$OUT"
