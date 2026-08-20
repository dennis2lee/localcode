#!/bin/sh
# Installs localcode into your own home directory. No root, no package
# manager, nothing written outside $HOME.
#
# The .deb is the better answer when you have root: it upgrades in place
# and uninstalls cleanly. This is for the machine where you do not, which
# on a shared or managed Ubuntu box is most of them. It puts one static
# binary in ~/.local/bin, the directory Ubuntu's own ~/.profile already
# adds to PATH when it exists.
#
#   curl -fsSL https://raw.githubusercontent.com/dennis2lee/localcode/main/scripts/install.sh | sh
#
# Options (pass them after "| sh -s --"):
#   --version x.y.z   install that release instead of the latest
#   --dir PATH        install somewhere else (default ~/.local/bin)
#   --uninstall       remove the binary this script installed
set -eu

REPO="${LOCALCODE_REPO:-dennis2lee/localcode}"
API="${LOCALCODE_API:-https://api.github.com}"
DIR="${LOCALCODE_DIR:-$HOME/.local/bin}"
VERSION=""
UNINSTALL=0

while [ $# -gt 0 ]; do
	case "$1" in
	--version) VERSION="${2:-}"; shift 2 ;;
	--version=*) VERSION="${1#*=}"; shift ;;
	--dir) DIR="${2:-}"; shift 2 ;;
	--dir=*) DIR="${1#*=}"; shift ;;
	--uninstall) UNINSTALL=1; shift ;;
	-h|--help) sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
	*) echo "install.sh: unknown option $1" >&2; exit 2 ;;
	esac
done

die() { echo "install.sh: $*" >&2; exit 1; }

if [ "$UNINSTALL" = 1 ]; then
	if [ -e "$DIR/localcode" ]; then
		rm -f "$DIR/localcode"
		echo "removed $DIR/localcode"
	else
		echo "nothing installed at $DIR/localcode"
	fi
	echo "your config and sessions are in ~/.localcode and were left alone"
	exit 0
fi

# fetch URL -> stdout. curl on most machines, wget on the ones that ship
# without it; both are checked because a container image usually has
# exactly one of them.
if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL "$1"; }
	fetch_to() { curl -fsSL -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -qO- "$1"; }
	fetch_to() { wget -qO "$2" "$1"; }
else
	die "neither curl nor wget is installed"
fi

case "$(uname -s)" in
Linux) OS=linux ;;
Darwin) OS=darwin ;;
*) die "no build for $(uname -s) — see https://github.com/$REPO/releases" ;;
esac

case "$(uname -m)" in
x86_64|amd64) ARCH=amd64 ;;
aarch64|arm64) ARCH=arm64 ;;
*) die "no build for $(uname -m) — see https://github.com/$REPO/releases" ;;
esac

if [ "$OS" = darwin ]; then
	# One universal binary, not one per architecture.
	SUFFIX="darwin-universal.tar.gz"
else
	SUFFIX="linux-$ARCH.tar.gz"
fi

if [ -n "$VERSION" ]; then
	REL_URL="$API/repos/$REPO/releases/tags/v$VERSION"
else
	REL_URL="$API/repos/$REPO/releases/latest"
fi

JSON="$(fetch "$REL_URL")" || die "could not reach $REL_URL"
TAG="$(printf '%s' "$JSON" | tr ',' '\n' | grep '"tag_name"' | head -1 | sed 's/.*: *"//;s/".*//')"
[ -n "$TAG" ] || die "no release found at $REL_URL"
VERSION="${TAG#v}"
ASSET="localcode-$VERSION-$SUFFIX"

# The URL is matched by the asset's own file name, and cut out with a
# pattern anchored on the field name: a greedy ".*:" would eat the "https:"
# in the value itself.
URL="$(printf '%s' "$JSON" | tr ',' '\n' | grep '"browser_download_url"' | grep "/$ASSET\"" | head -1 | sed 's/.*"browser_download_url":[[:space:]]*"//;s/".*//')"
[ -n "$URL" ] || die "release $TAG has no $ASSET"

# GitHub publishes a SHA-256 per asset. It sits in the same asset object,
# before that asset's download URL, so the last digest seen before our URL
# is ours.
DIGEST="$(printf '%s' "$JSON" | tr ',' '\n' | awk -v want="/$ASSET" '
	/"digest"/ { d = $0 }
	index($0, want) && /browser_download_url/ { print d; exit }
' | sed 's/.*sha256://;s/[^0-9a-fA-F].*//')"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

echo "==> downloading localcode $VERSION ($OS/$ARCH)"
fetch_to "$URL" "$TMP/$ASSET" || die "download failed: $URL"

if [ -n "$DIGEST" ]; then
	if command -v sha256sum >/dev/null 2>&1; then
		GOT="$(sha256sum "$TMP/$ASSET" | cut -d' ' -f1)"
	elif command -v shasum >/dev/null 2>&1; then
		GOT="$(shasum -a 256 "$TMP/$ASSET" | cut -d' ' -f1)"
	else
		GOT=""
		echo "    (no sha256sum or shasum here, so the download is unverified)"
	fi
	if [ -n "$GOT" ] && [ "$GOT" != "$DIGEST" ]; then
		die "checksum mismatch: expected $DIGEST, got $GOT"
	fi
else
	echo "    (this release publishes no checksum, so the download is unverified)"
fi

# Releases up to 0.50.0 were packed on a Mac with macOS extended
# attributes still in them, and GNU tar prints a line about each one
# ("Ignoring unknown extended header keyword ...") in the middle of the
# install, where it reads as a failure. Newer archives do not carry them;
# this silences that one warning for the older ones. GNU tar only: BSD
# tar has no --warning and does not complain in the first place.
TAR_QUIET=""
if tar --warning=no-unknown-keyword --version >/dev/null 2>&1; then
	TAR_QUIET="--warning=no-unknown-keyword"
fi
tar $TAR_QUIET -xzf "$TMP/$ASSET" -C "$TMP" localcode || die "the archive has no localcode binary in it"

mkdir -p "$DIR" || die "cannot create $DIR"
# Written beside the target and renamed into place: rename is atomic, so a
# localcode that is running right now keeps the file it started from
# instead of being replaced halfway through.
cp "$TMP/localcode" "$DIR/.localcode.new.$$"
chmod 0755 "$DIR/.localcode.new.$$"
mv "$DIR/.localcode.new.$$" "$DIR/localcode" || die "cannot write $DIR/localcode"

echo "==> installed $DIR/localcode ($("$DIR/localcode" version 2>/dev/null || echo "$VERSION"))"

case ":$PATH:" in
*":$DIR:"*) ;;
*)
	echo
	echo "$DIR is not on your PATH. Add it:"
	echo
	echo "    echo 'export PATH=\"$DIR:\$PATH\"' >> ~/.bashrc && . ~/.bashrc"
	echo
	echo "(Ubuntu's own ~/.profile puts ~/.local/bin on PATH, but only from"
	echo " the next login, so the line above is what makes this shell see it.)"
	;;
esac

if [ ! -f "$HOME/.localcode/config.json" ]; then
	echo
	echo "Next: write ~/.localcode/config.json — see"
	echo "https://github.com/$REPO/blob/main/docs/USAGE.md#config-file-configjson"
fi
