#!/usr/bin/env bash
# Is this localcode-gui.exe the one this release is being cut from?
#
# The failure it exists for has happened twice and is invisible when it
# does. CI builds from main, so a run dispatched before the release commit
# is pushed compiles the PREVIOUS release's code and stamps it with the new
# version: the workflow succeeds, the exe reports the right version, and
# the fix being shipped is simply absent from the desktop build.
#
# RELEASING.md told the operator to compare the run's headSha by hand, and
# the procedure it gave for finding the run is wrong: pushing and
# dispatching both fire the workflow, the two runs share a headSha, and
# `gh run list --limit 1` returns the push one. Picking a run is the whole
# difficulty, and this check does not pick one.
#
# The binary carries both facts itself. `go version -m` prints the ldflags
# it was linked with (`-X main.version=...`) and the commit it was built
# from (`vcs.revision=...`), so the artifact is asked directly and no run
# selection can get it wrong.
#
# Usage: scripts/check-gui-exe.sh <version> <path-to-localcode-gui.exe>
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:?usage: check-gui-exe.sh <version> <exe>}"
EXE="${2:?usage: check-gui-exe.sh <version> <exe>}"
fail() { echo "check-gui-exe: $*" >&2; exit 1; }

[ -f "$EXE" ] || fail "$EXE does not exist. Download it from the CI run: gh run download <run-id> -n localcode-gui-windows-amd64 -D /tmp/gui-exe"

info="$(go version -m "$EXE" 2>/dev/null)" || fail "$EXE is not a Go binary, or this Go toolchain cannot read it"

# 1. Stamped with the version being released. A run dispatched without
#    "-f version" falls back to `git describe`, which yields something like
#    "0.70.0-3-gf1fba88" — not "dev", not empty, so the workflow's own
#    smoke check passes it and the MSI ships a GUI naming the previous
#    release.
stamped="$(printf '%s\n' "$info" | sed -n 's/.*-X main.version=\([^ "]*\).*/\1/p' | head -1)"
if [ -z "$stamped" ]; then
	fail "$EXE carries no -X main.version at all, so it was not built as a release. Dispatch the workflow with: gh workflow run gui-windows.yml --ref main -f version=$VERSION"
fi
if [ "$stamped" != "$VERSION" ]; then
	fail "$EXE is stamped $stamped, and this release is $VERSION. Dispatch the workflow with -f version=$VERSION and download that run's artifact."
fi

# 4. -tags=gui is present in the build settings, and -H windowsgui is present
#    in the ldflags. Without the first the binary has no window at all:
#    internal/gui/stub.go is compiled in instead of gui.go, so Launch returns
#    an error and localcode falls back to the terminal interface. Without
#    the second it is a console-subsystem process that pins the terminal it
#    is launched from and flashes a console box when launched from Explorer.
if ! printf '%s\n' "$info" | grep -E -q 'build[[:space:]]+-tags=.*\<gui\>'; then
	fail "$EXE was built without -tags=gui, so it carries no window. Dispatch the workflow with -f version=$VERSION or compile with: go build -tags gui ..."
fi
if ! printf '%s\n' "$info" | grep -E -q -- '-H[[:space:]]+windowsgui'; then
	fail "$EXE ldflags lack -H windowsgui, so it will pin or flash a console window. Rebuild with: -ldflags \"-H windowsgui ...\""
fi

# 5. The PE subsystem is 2 (GUI). Read it from the file: the 4-byte offset
#    of the PE signature is at 0x3C, the COFF header is 20 bytes, and
#    Subsystem sits at offset 68 of the optional header — so the
#    little-endian 16-bit value at pe_offset + 24 + 68. 2 is GUI, 3 is
#    console. This check does not depend on the build settings being recorded
#    at all: an exe whose build settings were stripped or forged would still
#    be caught here if it is not a genuine GUI subsystem binary.
pe_offset="$(od -An -j 60 -N 4 -t u4 "$EXE" 2>/dev/null | tr -d '[:space:]')"
if [ -z "$pe_offset" ]; then
	fail "$EXE is not a valid PE binary: cannot read PE signature offset at 0x3C"
fi
subsystem_offset=$((pe_offset + 24 + 68))
subsystem="$(od -An -j "$subsystem_offset" -N 2 -t u2 "$EXE" 2>/dev/null | tr -d '[:space:]')"
if [ "$subsystem" != "2" ]; then
	fail "$EXE PE subsystem is ${subsystem:-unknown}, expected 2 (GUI). Rebuild with: -ldflags \"-H windowsgui ...\""
fi

# 2. Built from the commit being released. This is the half nothing has
#    ever checked, and the half that goes wrong.
built_from="$(printf '%s\n' "$info" | sed -n 's/.*vcs.revision=\([0-9a-f]*\).*/\1/p' | head -1)"
head="$(git rev-parse HEAD)"
if [ -z "$built_from" ]; then
	fail "$EXE records no vcs.revision, so there is no way to tell which commit it was built from"
fi
if [ "$built_from" != "$head" ]; then
	fail "$EXE was built from $built_from and HEAD is $head.
CI builds from main, so this exe compiled a different commit than the one being released — most likely the workflow was dispatched before the release commit was pushed.
Push first, dispatch again, and download the new run's artifact."
fi

# 3. Built from a clean tree. A dirty CI checkout would mean the artifact
#    does not correspond to any commit at all.
if printf '%s\n' "$info" | grep -q 'vcs.modified=true'; then
	fail "$EXE was built from a modified working tree, so it corresponds to no commit"
fi

echo "check-gui-exe: $(basename "$EXE") is v$VERSION built from ${head:0:7}, clean"
