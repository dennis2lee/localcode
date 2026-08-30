#!/usr/bin/env bash
# Does dist/ hold exactly the assets a release has to publish?
#
# Two properties, and the second is the sharper one.
#
# Completeness: update.AssetFor picks a download by platform and install
# shape, so an asset left out is not a missing file to the person on that
# platform — it is "release vX.Y.Z has nothing for linux/amd64", from a
# release that built it. The upload is a hand-typed list of nine paths in
# `gh release create`, and nothing has ever compared that list to what
# AssetFor can pick.
#
# Version: AssetFor matches by SUFFIX and never looks at the version in the
# name. A stale asset left in dist/ from a previous release therefore
# matches the same suffix, and whichever GitHub lists first wins — so a
# release could advertise x.y.z and hand somebody the previous build as an
# "update". Every name is checked to carry this version, and any file
# carrying another is an error rather than something to ignore.
#
# Usage: scripts/check-dist.sh <version> [dist-dir]
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:?usage: check-dist.sh <version> [dist-dir]}"
DIST="${2:-dist}"
fail() { echo "check-dist: $*" >&2; exit 1; }

# The nine, derived from AssetFor's suffix table in internal/update/update.go.
# Grouped by the question each answers, because that is what makes an
# omission legible rather than a name missing from a list.
required=(
	"mac/LocalCode-$VERSION-darwin-universal-app.tar.gz"     # darwin, installed as an .app
	"mac/localcode-$VERSION-darwin-universal.tar.gz"         # darwin, unpacked by hand
	"windows/localcode-$VERSION-windows-amd64.msi"           # windows/amd64, installed
	"windows/localcode-$VERSION-windows-amd64.zip"           # windows/amd64, unpacked
	"windows/localcode-$VERSION-windows-arm64.zip"           # windows/arm64
	"linux/localcode-$VERSION-linux-amd64.deb"               # linux/amd64, dpkg
	"linux/localcode-$VERSION-linux-amd64.tar.gz"            # linux/amd64, unpacked
	"linux/localcode-$VERSION-linux-arm64.deb"               # linux/arm64, dpkg
	"linux/localcode-$VERSION-linux-arm64.tar.gz"            # linux/arm64, unpacked
)

[ -d "$DIST" ] || fail "$DIST does not exist. Build it first: make dist VERSION=$VERSION GUI_EXE=..."

missing=()
for rel in "${required[@]}"; do
	[ -f "$DIST/$rel" ] || missing+=("$rel")
done
if [ ${#missing[@]} -ne 0 ]; then
	echo "check-dist: $DIST is missing ${#missing[@]} of ${#required[@]} assets:" >&2
	printf '  %s\n' "${missing[@]}" >&2
	fail "a platform whose asset is absent reads the release as having nothing for it"
fi

# Anything carrying another version. Not a warning: AssetFor would pick it
# by suffix and install it as this release.
strays="$(find "$DIST" -type f \( -name '*.msi' -o -name '*.zip' -o -name '*.deb' -o -name '*.tar.gz' \) \
	! -name "*-$VERSION-*" -print)"
if [ -n "$strays" ]; then
	echo "check-dist: these carry a version other than $VERSION:" >&2
	printf '%s\n' "$strays" | sed 's/^/  /' >&2
	fail "AssetFor matches by suffix and never reads the version, so one of these could be served as the $VERSION download. Remove them (make clean) and rebuild."
fi

echo "check-dist: all ${#required[@]} assets present for v$VERSION, none from another version"
printf '  %s\n' "${required[@]}"
