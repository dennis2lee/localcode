#!/usr/bin/env bash
# Release preflight: fails the build unless the docs were updated for the
# version being cut. Runs as a prerequisite of `make dist`, so a release
# tarball physically cannot be produced with stale docs. See RELEASING.md
# for the full checklist (the human-judgment parts a script can't verify).
set -euo pipefail

VERSION="${1:?usage: release-preflight.sh <version> [gui-exe]}"
GUI_EXE="${2:-}"
CHANGELOG="docs/CHANGELOG.md"
fail() { echo "release-preflight: $*" >&2; exit 1; }

# 1. CHANGELOG must have this version's entry, and it must be the top one —
#    so the notes were actually written for THIS release, not left under a
#    stale heading.
top_entry="$(grep -m1 '^## ' "$CHANGELOG" | sed 's/^## //')"
if [ "$top_entry" != "v$VERSION" ]; then
	fail "top CHANGELOG entry is '$top_entry', expected 'v$VERSION'. Promote the notes: rename '## Unreleased' (or add a new heading) to '## v$VERSION' at the top of $CHANGELOG."
fi

# 2. No stray '## Unreleased' left behind — that means notes were written
#    but never stamped with the version.
if grep -q '^## Unreleased' "$CHANGELOG"; then
	fail "'## Unreleased' still present in $CHANGELOG. Fold it into '## v$VERSION' before releasing."
fi

# 3. Every internal markdown link must resolve (file exists, #anchor exists).
#    A release that ships broken doc links is a bug we keep re-introducing.
python3 scripts/check-doc-links.py || fail "broken internal doc links (see above)."

# 4. The gate must have passed on THIS tree.
#
#    `make dist` used to expand to the preflight plus four packagers and
#    nothing else — no build, no vet, no test. Every guard in this
#    repository (the spawn-site AST walk, the prompt-asset inventory, the
#    config drift checks, the stylesheet and chrome geometry checks) runs
#    only under `go test`, so all of them were advisory: a release could
#    be cut with every one of them failing.
#
#    Re-running them here would double the release's cost, and the point
#    of scripts/check.sh is that it is fast. So the gate leaves a stamp
#    naming the tree it passed on, and this compares that stamp to the
#    tree being released. Cheap, and it cannot be satisfied by having run
#    the gate on something else.
stamp_file=".check-passed"
want="$(scripts/tree-id.sh)"
if [ ! -f "$stamp_file" ]; then
	fail "the checks have not been run on this tree. Run: make check"
fi
if [ "$(cat "$stamp_file")" != "$want" ]; then
	fail "$stamp_file is for a different tree than the one being released — something changed after the checks passed. Run: make check"
fi

# 5. The tools whose absence is a SKIP rather than a failure.
#
#    v0.36.0 shipped claiming the WebView2 bootstrapper's signature had
#    been verified. osslsigncode was missing on the release machine, the
#    check inside package-msi.sh skipped itself with a warning, the
#    warning scrolled past in a long build log, and the changelog said
#    otherwise. A check that can be skipped will be skipped; this is
#    where that stops being possible without noticing.
for tool in osslsigncode node; do
	command -v "$tool" >/dev/null 2>&1 || fail "$tool is not installed, and its absence makes a check skip itself silently rather than fail. Install it (brew install osslsigncode / node) before cutting a release."
done

# 6. The Windows GUI binary must be the one for this release.
#    See scripts/check-gui-exe.sh: it reads the version and the commit out
#    of the binary itself, so no amount of picking the wrong CI run can
#    satisfy it.
if [ -n "$GUI_EXE" ]; then
	scripts/check-gui-exe.sh "$VERSION" "$GUI_EXE" || fail "the GUI binary is not the one for this release (see above)."
fi

echo "release-preflight: docs, gate and artifacts OK for v$VERSION"
