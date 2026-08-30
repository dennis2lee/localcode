#!/usr/bin/env bash
# The gate: everything that has to pass before a commit, run at once.
#
# Sequentially this was a two-minute ritual typed from memory, and a ritual
# typed from memory is one somebody skips at the end of a long day. Almost
# none of it is actually ordered — a Windows cross-build has nothing to say
# to the Web UI suite — so it runs concurrently and the whole gate costs
# about what its slowest member costs.
#
# It does NOT run all nine at once, and the reason is worth writing down
# because "make it as parallel as possible" is the obvious wrong answer
# here. Every `go` command already fans out across every core on its own,
# so five of them at once do not share the machine, they oversubscribe it.
# Three schedules, two passes each, on an idle machine (seconds, mean):
#
#                            after an edit   nothing changed
#   all nine at once              14.5             13.0
#   Go serial, rest beside it     15.0             14.5
#   three lanes (this one)        13.0             12.5
#
# The spread is small when nothing else is running. It is not small when
# something else is: the same comparison taken while four agents were
# building in worktrees put all-at-once at 61s against 44s for the serial
# schedule. Lanes win on the quiet machine and degrade gracefully on the
# busy one, which is the combination worth having.
#
# So: three lanes, running concurrently, serial within each lane.
#   race  the race suite, alone, because it is the critical path and
#         everything else finishing early is worth nothing if it is slower
#   go    the four short Go commands, one after another beside it
#   misc  the checks that are not Go and barely register
#
# What the summary prints is each check's own duration, which is how the
# next person knows where the time actually goes rather than guessing.
#
# Usage:
#   scripts/check.sh            run everything
#   scripts/check.sh vet test   run only these
#   scripts/check.sh --list     name the checks and exit
set -uo pipefail

cd "$(dirname "$0")/.."

# Each entry is "lane<TAB>name<TAB>command", in the order the summary
# prints them.
#
# -count=1 on the race suite is deliberate. A cached PASS is a statement
# about a previous run of a previous tree, and the one thing a release gate
# must not do is report a result it did not produce.
#
# HEAD on the whitespace check is equally deliberate. Bare `git diff
# --check` compares the working tree against the index, so it inspects
# nothing at all once the changes are staged — and a release is cut from a
# tree that is committed, which is to say the state where the bare form
# has the least to say. Against HEAD it sees staged and unstaged alike.
checks=(
	"race	race	go test ./... -race -parallel 8 -count=1"
	"go	vet	go vet ./..."
	"go	gui	go build -tags gui ./..."
	"go	windows	GOOS=windows GOARCH=amd64 go build ./..."
	"go	linux	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./..."
	"misc	js	node --experimental-vm-modules --test test/webui/*.test.js"
	"misc	docs	python3 scripts/check-doc-links.py"
	"misc	fmt	scripts/check-fmt.sh"
	"misc	whitespace	git diff --check HEAD"
)
lanes="race go misc"

lane_of() { printf '%s' "${1%%	*}"; }
name_of() { local r="${1#*	}"; printf '%s' "${r%%	*}"; }
cmd_of() { local r="${1#*	}"; printf '%s' "${r#*	}"; }

if [ "${1:-}" = "--list" ]; then
	for c in "${checks[@]}"; do printf '%s\n' "$(name_of "$c")"; done
	exit 0
fi

# The gui build is CGo and links a native webview, so it can only be built
# for the machine it runs on. Skipped rather than failed off macOS, and it
# says so: a check that quietly vanishes is worse than one that is absent.
skip_gui=""
if [ "$(uname -s)" != "Darwin" ]; then
	skip_gui="not macOS (the gui build is CGo and cannot cross-compile)"
fi

want=("$@")
selected() {
	[ ${#want[@]} -eq 0 ] && return 0
	for w in "${want[@]}"; do [ "$w" = "$1" ] && return 0; done
	return 1
}

logdir="$(mktemp -d "${TMPDIR:-/tmp}/localcode-check.XXXXXX")"
trap 'rm -rf "$logdir"' EXIT

run_one() {
	local name="$1" cmd="$2" t0
	if [ "$name" = "gui" ] && [ -n "$skip_gui" ]; then
		printf 'skip\t0\t%s\n' "$skip_gui" > "$logdir/$name.status"
		return
	fi
	t0=$(date +%s)
	if eval "$cmd" > "$logdir/$name.log" 2>&1; then
		printf 'pass\t%s\t\n' "$(($(date +%s) - t0))" > "$logdir/$name.status"
	else
		printf 'fail\t%s\t\n' "$(($(date +%s) - t0))" > "$logdir/$name.status"
	fi
}

names=()
for entry in "${checks[@]}"; do
	name="$(name_of "$entry")"
	selected "$name" && names+=("$name")
done
if [ ${#names[@]} -eq 0 ]; then
	all=""
	for entry in "${checks[@]}"; do all="$all $(name_of "$entry")"; done
	printf 'check: %s names no check. Try one of:%s\n' "${want[*]}" "$all" >&2
	exit 2
fi

started=$(date +%s)
pids=()
for lane in $lanes; do
	(
		for entry in "${checks[@]}"; do
			[ "$(lane_of "$entry")" = "$lane" ] || continue
			name="$(name_of "$entry")"
			selected "$name" || continue
			run_one "$name" "$(cmd_of "$entry")"
		done
	) &
	pids+=("$!")
done
for pid in "${pids[@]}"; do wait "$pid"; done
elapsed=$(($(date +%s) - started))

failed=()
printf '\n'
for name in "${names[@]}"; do
	IFS=$'\t' read -r status secs note < "$logdir/$name.status"
	case "$status" in
	pass) mark="ok  " ;;
	skip) mark="skip" ;;
	*) mark="FAIL"; failed+=("$name") ;;
	esac
	if [ -n "${note:-}" ]; then
		printf '  %s  %-11s %3ss  %s\n' "$mark" "$name" "$secs" "$note"
	else
		printf '  %s  %-11s %3ss\n' "$mark" "$name" "$secs"
	fi
done

if [ ${#failed[@]} -eq 0 ]; then
	printf '\n  all checks passed in %ss\n' "$elapsed"
	# The stamp is what lets the release preflight know this gate ran
	# against THIS tree, without paying for it a second time. See
	# scripts/tree-id.sh and scripts/release-preflight.sh.
	#
	# Only ever written by a full run. "scripts/check.sh vet" passing is
	# not the gate passing, and a stamp that said otherwise would let a
	# release out on one check out of nine.
	if [ ${#want[@]} -eq 0 ]; then
		scripts/tree-id.sh > .check-passed
	else
		printf '  (a partial run leaves no stamp: the release gate needs all of them)\n'
	fi
	exit 0
fi

for name in "${failed[@]}"; do
	printf '\n===== %s =====\n' "$name"
	cat "$logdir/$name.log"
done
printf '\n  %d of %d checks failed after %ss: %s\n' \
	"${#failed[@]}" "${#names[@]}" "$elapsed" "${failed[*]}"
# A stale stamp would let a release go out on a gate that failed, so it is
# removed rather than left to be believed.
rm -f .check-passed
exit 1
