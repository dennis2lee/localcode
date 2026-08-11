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

# The sherpa-onnx DLLs the GUI needs at run time ship in the same CI
# artifact as localcode-gui.exe, so they are looked for beside it. Missing
# them is fatal rather than a warning: the MSI would install a GUI that
# cannot start, and the failure would surface as a bare Windows error box
# on a user's machine instead of here.
SHERPA_DIR="$(cd "$(dirname "$GUI_EXE_PATH")" && pwd)"
for dll in sherpa-onnx-c-api.dll sherpa-onnx-cxx-api.dll onnxruntime.dll; do
	if [ ! -f "$SHERPA_DIR/$dll" ]; then
		echo "error: $dll not found beside $GUI_EXE_PATH" >&2
		echo "       localcode-gui.exe cannot start without it. Download the whole" >&2
		echo "       gui-windows CI artifact, not just the .exe." >&2
		exit 1
	fi
done


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
	-D "SherpaDir=$SHERPA_DIR" \
	-o "$MSI" \
	build/localcode.wxs

# Let DICTATION cross to the elevated half of the install.
#
# Declaring the property is not enough. Windows Installer drops a
# command-line public property before the deferred actions unless it is
# listed in SecureCustomProperties, and InstallDictationModel's condition
# is evaluated there — so `msiexec /i ... DICTATION=0` was silently
# ignored on any install that went through UAC, and the ~200MB download it
# was told to skip happened anyway.
#
# Done here rather than in the .wxs because wixl writes that row itself
# from MajorUpgrade, and a second Property element with the same id makes
# it fail the build outright. So the generated value is read and DICTATION
# appended to it, keeping whatever wixl put there.
echo "==> allowing DICTATION through to the elevated install"
# Edited with a query rather than by re-importing the table: msibuild's
# .idt import wants a schema header msiinfo does not produce, and an
# UPDATE says exactly what is changing.
current="$(msiinfo export "$MSI" Property | awk -F'\t' '$1=="SecureCustomProperties"{print $2}')"
case "$current" in
*DICTATION*) ;;
"") msibuild "$MSI" -q "INSERT INTO \`Property\` (\`Property\`, \`Value\`) VALUES ('SecureCustomProperties', 'DICTATION')" ;;
*) msibuild "$MSI" -q "UPDATE \`Property\` SET \`Value\`='$current;DICTATION' WHERE \`Property\`='SecureCustomProperties'" ;;
esac

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

# The dictation model actions. These are checked because wixl drops a
# custom action it does not understand *without failing*: an earlier
# version of these used Directory=, which wixl has no property for, and
# it emitted the sequence rows while writing no CustomAction rows at all.
# The MSI built, installed, and did nothing.
#
# Type 3186 = EXE named by a property, deferred, no-impersonate,
# continue-on-failure. Property-sourced rather than FileKey-sourced on
# purpose: a FileKey action fails with error 2753 when the file's
# component is not part of the transaction, which on uninstall it never
# is.
verify_msi CustomAction 'SetDictationExe	51	DICTATIONEXE	[INSTALLDIR]localcode.exe' 'the property that points the dictation actions at localcode.exe is missing'
verify_msi CustomAction 'InstallDictationModel	3186	DICTATIONEXE' 'the dictation model install action is missing or has the wrong type'
verify_msi CustomAction 'RemoveDictationModel	3186	DICTATIONEXE' 'the dictation model removal action is missing or has the wrong type'
# Skippable, and skipped only when asked: DICTATION defaults to 1.
verify_msi Property 'DICTATION	1' 'the DICTATION property is missing, so the model install cannot be turned off with DICTATION=0'
# And that it is allowed to cross to the elevated half of the install.
# Declaring the property is not enough: without this row Windows Installer
# drops a command-line DICTATION=0 before the deferred action that reads
# it, so the download happens anyway and the check above passes while the
# switch does nothing. That is what the previous release shipped.
# Checked against that row specifically, not as a substring of the whole
# table: the table also contains a DICTATION row of its own, so a
# substring match would pass while the switch stayed broken — which is the
# same shape as the fault being fixed.
secure="$(msiinfo export "$MSI" Property | awk -F'\t' '$1=="SecureCustomProperties"{print $2}')"
case "$secure" in
*DICTATION*) ;;
*)
	echo "MSI verification failed: DICTATION is not in SecureCustomProperties, so DICTATION=0 is silently ignored on an elevated install" >&2
	echo "  SecureCustomProperties = $secure" >&2
	exit 1
	;;
esac
# An upgrade must not delete the model and download it again. See the
# comment on this condition in localcode.wxs.
verify_msi InstallExecuteSequence 'RemoveDictationModel	REMOVE="ALL" AND NOT UPGRADINGPRODUCTCODE' 'the model removal is not guarded against upgrades, so every upgrade would re-download 400MB'
# The desktop shortcut, and that it resolves to DesktopFolder rather than
# some directory wixl invented. A shortcut silently landing nowhere is
# exactly the kind of breakage this file exists to catch — it cannot be
# noticed from a build machine that isn't Windows.
verify_msi Shortcut 'LocalCodeDesktopShortcut	DesktopFolder' 'the desktop shortcut is missing or not pointing at DesktopFolder'
# The icon has to be embedded and referenced, or every shortcut falls back
# to the exe's default (which, for a Go binary with no resource section,
# is the blank Windows one).
verify_msi File 'sherpa-onnx-c-api.dll' 'the sherpa-onnx runtime is missing — localcode-gui.exe will not start'
verify_msi File 'onnxruntime.dll' 'onnxruntime.dll is missing — localcode-gui.exe will not start'
verify_msi Icon 'LocalCodeIcon' 'the application icon is missing from the Icon table'
verify_msi Property 'ARPPRODUCTICON	LocalCodeIcon' 'Add/Remove Programs is not pointed at the application icon'

echo "==> done: $MSI"
ls -la "$MSI"
