# AGENTS.md

Project rules for agents working in this repo.

## Build and test

* `go build ./...` && `go vet ./...` && `go test ./... -race -parallel 8` before any commit.
* Keep the default build pure Go. The GUI (`internal/gui`) is behind the `gui` build tag and uses CGo; never make a non-tagged package import it.
* Cross-compile checks, both must pass: `GOOS=windows GOARCH=amd64 go build ./cmd/localcode` and `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/localcode`. Windows and Linux are both release targets built from this machine.
* Changing `internal/daemon/static/` means running the Web UI suite: `make test-js` (also runs inside `go test ./internal/daemon/`, and skips itself when `node` is missing).

## Parallel agents: isolate them, or do not let them near the tree

An agent fanned out to read this repo runs in the same working directory
as whoever fanned it out. That is one shared, mutable tree. It has already
gone wrong once: a read-only scout wanted to compile-check a probe, left a
`zz_probe_test.go` behind, and ran `git checkout --` on
`internal/provider/bedrock.go` to clean up after itself — while that file
was being edited. Nothing was lost, and only because the agent reported
what it had done.

* **A scout that reads the repo gets `isolation: "worktree"`.** It cannot
  reach the tree being worked in, and it reads a clean `HEAD` — which is
  also the more useful thing for it to report against, since a tree that
  moves under an agent makes every line number it quotes a guess.
* **A reviewer of uncommitted work cannot use a worktree**, because a
  worktree is `HEAD` and the work is not in it yet. Commit first and let
  it review the commit, or hand it the diff. Do not point it at the live
  tree and rely on instructions to keep it read-only.
* **Say which in the prompt either way.** "Do not modify any file, do not
  run git commands that write" costs one line and is the backstop for
  whichever of the two applies.

The cost of a worktree is a few hundred milliseconds and some disk per
agent, and it is auto-removed when unchanged. That is nothing against one
lost edit.

**A worktree isolates the working tree and the index, and nothing else.**
All worktrees share one object store and one set of refs, so a command
that writes those still crosses the boundary: `git branch -D`, `git gc`,
`git reflog expire`, rewriting a ref another worktree is checked out on.
Creating files and `git checkout -- <path>` — the two that caused the
incident above — are safely inside the boundary. Anything that rewrites
history is not, and no agent should be running those here anyway.

## Releasing — docs are mandatory

**Never cut a release without updating the docs.** `make dist` runs
`scripts/release-preflight.sh` and refuses to build if the CHANGELOG is stale or
doc links are broken, but the preflight cannot judge everything. Every release,
follow [RELEASING.md](RELEASING.md) in full. In particular, each release:

* Add a `## vX.Y.Z` entry at the top of `docs/CHANGELOG.md` (fold any `## Unreleased`).
* Re-read the **README.md feature table** against what shipped and update it. This one goes stale the most.
* Flip any now-done item in **docs/IMPROVEMENTS.md** to done with the version.
* Document new commands, flags, config keys, and behavior changes in **docs/USAGE.md**; add new config keys to **config.example.json**.
* Upload every asset the release builds. The in-app update check asks GitHub for the asset list and picks by platform and install shape, so one left out reads as "this release has nothing for your platform".

## Doc style

English only. Prefer tables and bullet lists over prose. No em dashes; use `*` for bullets; hyphens only inside literal flags/IDs.
