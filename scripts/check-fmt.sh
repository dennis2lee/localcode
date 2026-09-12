#!/usr/bin/env bash
# gofmt over the whole repo, as a check rather than a listing.
#
# `gofmt -l` prints names and exits 0 whether or not it found any, so used
# directly it is a check that can never fail. This is the wrapper that
# makes it one.
#
# It has to make it one twice. The first version sent stderr to /dev/null
# and forced the exit code to 0 so the wrapper could read the listing
# without set -e ending it, which recreated the same hole for the other
# way gofmt fails: a file that will not parse is reported on stderr with
# exit 2 and never appears in the listing, so the check passed on a file
# that is not Go. Both streams are kept now, and either one failing fails.
set -euo pipefail
cd "$(dirname "$0")/.."

errors="$(mktemp)"
trap 'rm -f "$errors"' EXIT

set +e
unformatted="$(gofmt -l cmd internal test build 2>"$errors")"
status=$?
set -e

if [ -s "$errors" ] || [ "$status" -ne 0 ]; then
	echo "gofmt could not read some files:"
	sed 's/^/  /' "$errors"
	echo
	echo "a file that will not parse is not a formatting problem — fix the syntax."
	exit 1
fi

if [ -n "$unformatted" ]; then
	echo "these files are not gofmt'd:"
	echo "$unformatted" | sed 's/^/  /'
	echo
	echo "fix with: gofmt -w $(echo "$unformatted" | tr '\n' ' ')"
	exit 1
fi
