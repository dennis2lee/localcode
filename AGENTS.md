# AGENTS.md

Project rules for agents working in this repo.

## Build and test

* **`make check` before any commit.** It runs everything below at once and
  reports each check with its own duration; `./scripts/check.sh --list`
  names them and `./scripts/check.sh vet` runs one. It is also the thing
  that makes a release possible: it stamps the tree it passed on, and
  `make dist` refuses to build unless that stamp matches.
* What it runs, and why each is in it rather than left to memory:
  * `go test ./... -race -parallel 8 -count=1` — 1,182 tests over 30
    packages. `-count=1` matters: a cached PASS is a statement about a
    previous run of a previous tree. `-parallel 8` does not: it bounds
    only the tests that call `t.Parallel()`, of which this repo has ten,
    all in `internal/config`. The suite's concurrency is `go test` running
    package binaries side by side, which defaults to the core count.
    Measured: `-parallel 1` 10s, `-parallel 8` 9s, `-p 1` 61s.
  * `go vet ./...`, `go build -tags gui ./...` (macOS only, CGo), and both
    cross-builds — `GOOS=windows GOARCH=amd64` and `CGO_ENABLED=0
    GOOS=linux GOARCH=amd64`. Windows and Linux are release targets built
    from this machine, so a break in either is a break in the release.
  * `scripts/check-deadcode.sh` — `go tool deadcode` over `./cmd/localcode`,
    diffed against `scripts/deadcode.allow`. It fails on a function
    nothing reaches that is not already known, and on an allowlist entry
    that is no longer reported. The "written, tested, never called" shape
    has shipped twice (v0.55.0, v0.57.0); `-test=false` is what makes it
    visible, at the cost of listing the functions only tests call.
  * The Web UI suite (270 tests, deliberately also run by `TestWebUI`,
    which skips itself when node is absent), the doc-link checker,
    `gofmt`, and `git diff --check HEAD` — `HEAD` because the bare form
    compares against the index and so inspects nothing once changes are
    staged, which is the state a release is cut from.
* Keep the default build pure Go. The GUI (`internal/gui`) is behind the `gui` build tag and uses CGo; never make a non-tagged package import it.
* Roughly twenty tests in this repo police the repository rather than the
  product: an AST walk over every process-spawn site, the prompt-asset
  inventory in both directions, config drift against the built-in roster,
  the stylesheet's custom properties, the GUI chrome geometry against the
  CSS. They only ever run under `go test`, which is why the gate is
  wired into `make dist` rather than trusted to a habit.

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

* English only, except files explicitly maintained as translations.
* BLUF structure: conclusion or required action first, supporting detail later.
* Direct engineering language. No metaphors, promotional phrasing, filler, or implied claims.
* One statement per sentence. Keep the subject and condition explicit.
* Prefer short noun phrases, `*` bullets, and tables for repeated fields or comparisons.
* Use prose only when sequence, rationale, or constraints need explanation.
* No em dashes. Use a period, colon, comma, or parentheses as appropriate.
* Hyphens only inside literal flags, IDs, compound terms, and quoted source text.
* Preserve audit records. Style edits must not change a historical finding, disposition, date, or cited evidence.
