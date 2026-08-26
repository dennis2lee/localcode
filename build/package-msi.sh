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
# Every other downloaded artifact in this project is checksum-pinned, and
# this is the one that gets embedded in a per-machine installer and run
# elevated on someone else's computer. Microsoft serves this URL as a
# rolling "evergreen" bootstrapper, so a hash pin would break on every
# upstream refresh; the signature is the stable thing about it.
#
# What is checked, and what is not, because a blanket `osslsigncode
# verify` fails on this exact file for two reasons that are not the file's
# fault:
#
#   * It carries TWO signatures. The primary one is Microsoft Corporation,
#     issued by Microsoft Code Signing PCA 2024 and timestamped. The
#     second is a self-signed internal "EdgeBuild" certificate, which no
#     chain check can ever accept.
#   * Chain validation needs Microsoft's code-signing roots, which a Mac
#     does not have in /etc/ssl/cert.pem.
#
# So the check asserts the two things that ARE verifiable here: the bytes
# match the digest inside the signature, and a signer is Microsoft
# Corporation. That is not a full trust-chain validation and is not
# claimed to be — it is HTTPS from go.microsoft.com plus proof the file
# was not altered after Microsoft signed it.
echo "==> checking the WebView2 bootstrapper's signature"
if command -v osslsigncode >/dev/null 2>&1; then
	sig="$(osslsigncode verify -in "$WORK/MicrosoftEdgeWebview2Setup.exe" 2>&1 || true)"
	if ! printf '%s' "$sig" | grep -q 'Subject: CN=Microsoft Corporation,'; then
		echo "error: the WebView2 bootstrapper is not signed by Microsoft Corporation" >&2
		echo "       it is embedded in the MSI and run elevated; refusing to ship it" >&2
		printf '%s\n' "$sig" >&2
		exit 1
	fi
	# Every reported digest pair has to agree. A mismatch means the file
	# was changed after it was signed.
	if printf '%s' "$sig" | awk '
		/Current message digest/    { cur = $NF }
		/Calculated message digest/ { if ($NF != cur) bad = 1 }
		END { exit bad ? 0 : 1 }
	'; then
		echo "error: the WebView2 bootstrapper does not match its own signature" >&2
		echo "       it is embedded in the MSI and run elevated; refusing to ship it" >&2
		printf '%s\n' "$sig" >&2
		exit 1
	fi
	echo "    signed by Microsoft Corporation, contents match the signature"
else
	echo "    warning: osslsigncode not installed, so the bootstrapper was NOT checked" >&2
	echo "             install it with: brew install osslsigncode" >&2
fi

MSI="$OUT/${BIN_NAME}-${VERSION}-windows-amd64.msi"
echo "==> wixl -> $MSI"
wixl -a x64 \
	-D "Version=${VERSION}" \
	-D "ExePath=$WORK/${BIN_NAME}.exe" \
	-D "GuiExePath=$GUI_EXE_PATH" \
	-D "WebView2BootstrapperPath=$WORK/MicrosoftEdgeWebview2Setup.exe" \
	-D "IconPath=$ROOT/build/icon/localcode.ico" \
	-o "$MSI" \
	build/localcode.wxs

# Verify what actually landed in the MSI database rather than trusting that
# wixl did what the .wxs said. Two things have silently gone wrong here
# before: a custom action wixl couldn't express at all (it warned but still
# exited 0, leaving an empty CustomAction table), and a condition mangled by
# the preprocessor eating a lone "$".
#
# Both produce an MSI that installs but misbehaves, on a platform this build
# machine can't run — so the checks are the only feedback available.
echo "==> verifying MSI tables"
verify_msi() {
	local table="$1" pattern="$2" what="$3" dump
	dump="$(msiinfo export "$MSI" "$table")"
	# Matched with a shell case rather than `| grep -q`: this script runs
	# under `set -o pipefail`, and grep -q exits at the first match, which
	# SIGPIPEs msiinfo mid-write and makes the pipeline report failure even
	# when the pattern was found.
	case "$dump" in
	*"$pattern"*) ;;
	*)
		echo "MSI verification failed: $what" >&2
		echo "  expected to find in the $table table: $pattern" >&2
		echo "$dump" >&2
		exit 1
		;;
	esac
}

# The WebView2 bootstrapper must be present as an installable file...
verify_msi File 'MicrosoftEdgeWebview2Setup.exe' 'the WebView2 bootstrapper is missing from the File table'
# ...the custom action that runs it must exist (type 3154 = EXE from the
# File table, deferred, no-impersonate, continue-on-failure)...
verify_msi CustomAction 'InstallWebView2Runtime	3154	WebView2BootstrapperEXE' 'the WebView2 custom action is missing or has the wrong type'
# ...and its condition must have survived the preprocessor with the "$"
# intact. Without the "$" this reads as an undefined property, is always
# false, and the runtime is never installed — silently.
verify_msi InstallExecuteSequence '$WebView2BootstrapperFile=3' 'the WebView2 custom action condition lost its "$" (wixl preprocessor ate it — escape it as "$$" in the .wxs)'
# Both binaries ship.
verify_msi File 'localcode.exe' 'localcode.exe is missing from the File table'
verify_msi File 'localcode-gui.exe' 'localcode-gui.exe is missing from the File table'

# The desktop shortcut, and that it resolves to DesktopFolder rather than
# some directory wixl invented. A shortcut silently landing nowhere is
# exactly the kind of breakage this file exists to catch — it cannot be
# noticed from a build machine that isn't Windows.
verify_msi Shortcut 'LocalCodeDesktopShortcut	DesktopFolder' 'the desktop shortcut is missing or not pointing at DesktopFolder'
# The icon has to be embedded and referenced, or every shortcut falls back
# to the exe's default (which, for a Go binary with no resource section,
# is the blank Windows one).
verify_msi Icon 'LocalCodeIcon' 'the application icon is missing from the Icon table'
verify_msi Property 'ARPPRODUCTICON	LocalCodeIcon' 'Add/Remove Programs is not pointed at the application icon'

echo "==> done: $MSI"
ls -la "$MSI"
