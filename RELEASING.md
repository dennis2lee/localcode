# Releasing

Cutting a release is not just tagging a build. **Docs are part of the release.**
The `dist` target refuses to build until the mechanical checks below pass, and
the rest are your responsibility every single time.

## Mandatory, before `make dist VERSION=x.y.z`

Walk this whole list. The ones marked (auto) are enforced by
`scripts/release-preflight.sh` and will fail the build if skipped; the rest a
script cannot judge, so they are on you.

1. **docs/CHANGELOG.md (auto)** — add a `## vX.Y.Z` entry at the very top,
   describing every user-facing change since the last release. Fold any
   `## Unreleased` section into it. The preflight fails if the top entry is not
   `vX.Y.Z` or if `## Unreleased` is left behind.
2. **Internal doc links (auto)** — `scripts/check-doc-links.py` must pass (no
   broken file paths or `#anchors`).
3. **README.md feature table** — if the release adds, removes, or changes a
   user-visible capability, the feature table must reflect it. This is the one
   that keeps going stale. Read it against what shipped, do not assume.
4. **docs/IMPROVEMENTS.md** — if the release fixed something listed under
   "Remaining work" or an "Idea", flip that item to done (strike it and note the
   version). If it revealed a new gap, add it.
5. **docs/USAGE.md** — new command, flag, config key, or behavior change gets
   documented or updated here. New `/config` setting, new `config.json` field,
   new CLI flag: all land in USAGE.
6. **docs/MODELS.md** — only if provider/model setup or IDs changed.
7. **config.example.json** — if a new config key shipped, show it here (off or
   with a safe default).

## A check that can be skipped will be skipped

`package-msi.sh` verifies the WebView2 bootstrapper's signature only when
`osslsigncode` is installed, and otherwise prints a warning and carries
on. v0.36.0 shipped that way: the tool was missing on the release
machine, the warning scrolled past in a long build log, and the changelog
claimed the signature was verified. The check itself was also wrong, and
nothing had ever run it.

So: **if a release note says something was verified, the release build has
to have done the verifying.** Before `make dist`, confirm the tools the
build's own checks need are present:

```bash
command -v osslsigncode wixl msiinfo msibuild node
```

## Style rules for all docs

* English only. One exception: a page may carry a translation beside it
  (`docs/<name>.ko.html`). The English page is the source of truth and is
  what the other docs link to; a translation is added and updated with it,
  never instead of it. Do not translate a doc in place.
* Prefer tables and bullet lists over prose.
* No em dashes. Use `*` for bullets. Hyphens only inside literal flags/IDs.

## Then

**Commit and push the release first.** CI builds from `main`, so a run
dispatched before the push compiles the *previous* release's code and stamps
it with the new version. Nothing catches that: the workflow succeeds, the exe
reports the right version, and the fix you are shipping is simply absent from
the desktop build. Check the run's `headSha` against your commit.

```bash
make check                 # the whole gate, in parallel, ~15s warm
git add -A && git commit   # code + docs together, never docs "later"
git push origin main
```

`make check` is not advisory. It leaves a stamp naming the tree it passed
on, and `make dist` refuses to build unless that stamp matches the tree
being released — which is what makes this repository's twenty-odd guard
tests (the spawn-site AST walk, the prompt-asset inventory, the config
drift checks, the stylesheet and chrome geometry checks) part of a
release rather than something to remember.

The Windows MSI bundles `localcode-gui.exe`, which is CGo and cannot be
cross-compiled from macOS. Get a build from CI once main has the commit:

Dispatch it with the version you are releasing rather than reusing the last
push build. That binary is stamped with `-X main.version`, and it is the one
the GUI header and `localcode-gui.exe version` report; a run off `main` only
knows the *previous* tag, so it would ship a release whose desktop build
names the version before it.

```bash
gh workflow run gui-windows.yml --ref main -f version=x.y.z
gh run list --workflow=gui-windows.yml --limit 5 --json databaseId,event,status,conclusion,headSha
gh run download <run-id> -n localcode-gui-windows-amd64 -D /tmp/gui-exe
```

**Take the `workflow_dispatch` run, not the first one.** Pushing the
release commit also triggers this workflow, so two runs appear with the
same `headSha` and the push one sorts first — and only the dispatched one
carries the version input. `--limit 1` returns the wrong run.

You do not have to get that right by eye. `make dist` reads the version
and the commit out of the downloaded binary itself (`go version -m`) and
refuses one that was built from another commit or stamped with another
version, which is the failure this whole dance exists to avoid and the one
that has actually happened.

```bash
make dist VERSION=x.y.z GUI_EXE=/tmp/gui-exe/localcode-gui.exe  # runs the preflight first; refuses if docs are stale
gh release create vx.y.z \
  dist/windows/localcode-x.y.z-windows-amd64.msi \
  dist/windows/localcode-x.y.z-windows-amd64.zip \
  dist/windows/localcode-x.y.z-windows-arm64.zip \
  dist/linux/localcode-x.y.z-linux-amd64.deb \
  dist/linux/localcode-x.y.z-linux-arm64.deb \
  dist/linux/localcode-x.y.z-linux-amd64.tar.gz \
  dist/linux/localcode-x.y.z-linux-arm64.tar.gz \
  dist/mac/localcode-x.y.z-darwin-universal.tar.gz \
  dist/mac/LocalCode-x.y.z-darwin-universal-app.tar.gz \
  --repo dennis2lee/localcode --title "vx.y.z" --notes-file notes.md
rm -rf dist
```

**Publish every asset `AssetFor` knows how to pick.** The update check in
the settings window asks GitHub for the release's asset list and looks for
the one matching this platform and install shape — the `.msi`, either macOS
tarball, and on Linux either the `.deb` or the tarball. An asset left out of
the upload is not a missing file to the person on that platform: it is
"release vx.y.z has nothing for linux/amd64", from a release that built it.
