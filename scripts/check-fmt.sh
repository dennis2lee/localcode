#!/usr/bin/env bash
# gofmt over the whole repo, as a check rather than a listing.
#
# `gofmt -l` prints names and exits 0 whether or not it found any, so used
# directly it is a check that can never fail. This is the wrapper that
# makes it one.
set -euo pipefail
cd "$(dirname "$0")/.."

unformatted="$(gofmt -l cmd internal test build 2>/dev/null || true)"
if [ -n "$unformatted" ]; then
	echo "these files are not gofmt'd:"
	echo "$unformatted" | sed 's/^/  /'
	echo
	echo "fix with: gofmt -w $(echo "$unformatted" | tr '\n' ' ')"
	exit 1
fi
