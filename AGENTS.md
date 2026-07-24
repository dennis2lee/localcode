# AGENTS.md

Project rules for agents working in this repo.

## Build and test

* `go build ./...` && `go vet ./...` && `go test ./... -race -parallel 8` before any commit.
* Keep the default build pure Go. The GUI (`internal/gui`) is behind the `gui` build tag and uses CGo; never make a non-tagged package import it.
* Cross-compile check for Windows: `GOOS=windows GOARCH=amd64 go build ./cmd/localcode` must pass.

## Releasing — docs are mandatory

**Never cut a release without updating the docs.** `make dist` runs
`scripts/release-preflight.sh` and refuses to build if the CHANGELOG is stale or
doc links are broken, but the preflight cannot judge everything. Every release,
follow [RELEASING.md](RELEASING.md) in full. In particular, each release:

* Add a `## vX.Y.Z` entry at the top of `docs/CHANGELOG.md` (fold any `## Unreleased`).
* Re-read the **README.md feature table** against what shipped and update it. This one goes stale the most.
* Flip any now-done item in **docs/IMPROVEMENTS.md** to done with the version.
* Document new commands, flags, config keys, and behavior changes in **docs/USAGE.md**; add new config keys to **config.example.json**.

## Doc style

English only. Prefer tables and bullet lists over prose. No em dashes; use `*` for bullets; hyphens only inside literal flags/IDs.
