#!/usr/bin/env bash
# Tests build/msi-checks.sh against canned InstallExecuteSequence dumps.
# Run: build/msi-checks-test.sh. Exit nonzero on the first failure.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$ROOT/msi-checks.sh"

pass=0
fail=0
check() { # name, want(0/1), dump...
	local name="$1" want="$2"
	shift 2
	if printf '%s' "$1" | rep_order_ok; then got=0; else got=1; fi
	if [ "$got" = "$want" ]; then
		pass=$((pass + 1))
	else
		fail=$((fail + 1))
		echo "FAIL: $name (want exit $want, got $got)"
	fi
}

LATE='Action	Condition	Sequence
InstallExecuteSequence	Action
PublishProduct		6400
InstallExecute		6500
RemoveExistingProducts		6550
InstallFinalize		6600
'
EARLY='Action	Condition	Sequence
InstallExecuteSequence	Action
InstallValidate		1400
RemoveExistingProducts		1401
InstallInitialize		1500
InstallFinalize		6600
'
NOEXECUTE='Action	Condition	Sequence
InstallExecuteSequence	Action
RemoveExistingProducts		6550
InstallFinalize		6600
'

check "late removal passes" 0 "$LATE"
check "early removal fails" 1 "$EARLY"
check "missing InstallExecute fails" 1 "$NOEXECUTE"

echo "pass=$pass fail=$fail"
[ "$fail" = 0 ]
