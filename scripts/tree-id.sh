#!/usr/bin/env bash
# An identity for the working tree as it stands: every file that makes up
# the build, by path, mode and contents.
#
# It exists so the release preflight can tell whether the gate that wrote
# a stamp ran against the tree being released, without re-running the
# gate.
#
# It hashes CONTENT, not (HEAD + diff), and that distinction is the whole
# design. The first version of this hashed `git rev-parse HEAD` plus `git
# diff HEAD`, which meant `git commit` changed the identity of a tree
# whose files had not changed by one byte — the diff simply moved into
# HEAD. Since the release commit is made before `make dist` runs, the
# stamp was dead on arrival every single time, and the gate would have
# had to be run twice per release. Content-addressing makes committing,
# amending, and rebasing invisible to it, which is correct: none of them
# change what compiles.
#
# Untracked files are included because a new .go file changes the build,
# and deleted-but-tracked files are recorded as absent because removing
# one does too.
set -euo pipefail
cd "$(dirname "$0")/.."

# The file count is emitted alongside the hash stream and checked below.
# The second version of this script died at its first non-executable file
# — `printf '- '` reads the '-' as an option, and under `set -e` that
# ended the loop — so it hashed a constant prefix of the tree and stopped
# responding to edits entirely, while still printing a plausible hash. A
# tree identity that quietly stops identifying the tree is worse than no
# stamp at all, since the preflight believes it.
count=0
digest="$(
	git ls-files -z --cached --others --exclude-standard |
		LC_ALL=C sort -z -u |
		while IFS= read -r -d '' f; do
			# The path is hashed as well as the contents: swapping the
			# names of two files leaves both content hashes unchanged.
			# (No apostrophes in the comments inside this $( ): bash
			# 3.2, which is what macOS ships, scans a command
			# substitution for its closing paren while tracking quotes
			# and does not skip comments while doing it, so one
			# apostrophe here is an unterminated string.)
			printf '%s\n' "$f"
			if [ -L "$f" ]; then
				printf 'symlink %s\n' "$(readlink "$f")"
			elif [ ! -e "$f" ]; then
				# Tracked and deleted from the working tree.
				printf 'absent\n'
			else
				# The executable bit is part of the build for the
				# scripts/ and build/ directories.
				if [ -x "$f" ]; then mode=x; else mode=-; fi
				printf '%s ' "$mode"
				shasum -a 256 < "$f" | cut -d' ' -f1
			fi
		done
)"
count="$(printf '%s\n' "$digest" | grep -c '^' || true)"

if [ "$count" -lt 100 ]; then
	echo "tree-id: only $count lines of tree digest, which cannot be right for this repository — the walk stopped early" >&2
	exit 1
fi
printf '%s\n' "$digest" | shasum -a 256 | cut -d' ' -f1
