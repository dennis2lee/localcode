#!/usr/bin/env bash
# Fail on a function that nothing reaches from cmd/localcode, unless it is
# one scripts/deadcode.allow already knows about.
#
# The shape this catches has recurred. v0.55.0 round 1: "the classifier
# that decides which failures another endpoint could survive was written,
# documented and unit tested, and neither fallback call site called it",
# so every malformed request walked the whole fallback chain. v0.57.0: the
# activation snapshot declared the advertised tools, the fallback position
# and the feature flags, and nothing set any of them, so every manifest
# claimed chain position zero and no tools. Each time the answer was a
# test written for that one area afterwards. This is the general form.
#
# -test=false is deliberate. With tests included, deadcode counts a test
# as a caller and reports almost nothing -- which would hide precisely the
# "written, tested, never called" shape that motivated this. The cost is
# that a function only tests call is reported, and those are in the
# allowlist under their own heading.
#
# The tool is pinned as a tool dependency in go.mod (`go get -tool`), so
# this runs the same version everywhere and needs no network beyond what
# building the project already needs.
set -uo pipefail
cd "$(dirname "$0")/.."

allow="scripts/deadcode.allow"
[ -f "$allow" ] || { echo "check-deadcode: $allow is missing" >&2; exit 1; }

report="$(go tool deadcode -test=false ./cmd/localcode 2>&1)" || {
	echo "check-deadcode: deadcode did not run:" >&2
	printf '%s\n' "$report" >&2
	exit 1
}

# "internal/agent/loop.go:577:16: unreachable func: Loop.systemPromptFor"
# becomes "internal/agent/loop.go:Loop.systemPromptFor" -- the line number
# is dropped so that editing a file above a function does not churn the
# allowlist.
found="$(printf '%s\n' "$report" |
	sed -n 's|^\([^:]*\):[0-9]*:[0-9]*: unreachable func: \(.*\)$|\1:\2|p' |
	LC_ALL=C sort -u)"

allowed="$(sed 's/#.*//' "$allow" | sed 's/[[:space:]]*$//' | grep -v '^$' | LC_ALL=C sort -u)"

new="$(comm -23 <(printf '%s\n' "$found") <(printf '%s\n' "$allowed"))"
stale="$(comm -13 <(printf '%s\n' "$found") <(printf '%s\n' "$allowed"))"

status=0
if [ -n "$new" ]; then
	echo "check-deadcode: nothing reaches these from cmd/localcode:" >&2
	printf '%s\n' "$new" | sed 's/^/  /' >&2
	echo "" >&2
	echo "Either wire it up, delete it, or add it to $allow with a line saying why." >&2
	status=1
fi
if [ -n "$stale" ]; then
	echo "check-deadcode: $allow lists these and deadcode no longer reports them:" >&2
	printf '%s\n' "$stale" | sed 's/^/  /' >&2
	echo "" >&2
	echo "They have a caller again, or they are gone. Remove them from $allow: an allowlist nobody prunes stops meaning anything." >&2
	status=1
fi

[ $status -eq 0 ] && echo "deadcode OK ($(printf '%s\n' "$allowed" | grep -c '^') known, none new)"
exit $status
