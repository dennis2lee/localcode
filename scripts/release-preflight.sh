#!/usr/bin/env bash
# Release preflight: fails the build unless the docs were updated for the
# version being cut. Runs as a prerequisite of `make dist`, so a release
# tarball physically cannot be produced with stale docs. See RELEASING.md
# for the full checklist (the human-judgment parts a script can't verify).
set -euo pipefail

VERSION="${1:?usage: release-preflight.sh <version>}"
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

echo "release-preflight: docs OK for v$VERSION"
