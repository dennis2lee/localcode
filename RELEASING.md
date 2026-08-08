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

## Style rules for all docs

* English only.
* Prefer tables and bullet lists over prose.
* No em dashes. Use `*` for bullets. Hyphens only inside literal flags/IDs.

## Then

**Commit and push the release first.** CI builds from `main`, so a run
dispatched before the push compiles the *previous* release's code and stamps
it with the new version. Nothing catches that: the workflow succeeds, the exe
reports the right version, and the fix you are shipping is simply absent from
the desktop build. Check the run's `headSha` against your commit.

```bash
git add -A && git commit   # code + docs together, never docs "later"
git push origin main
```

The Windows MSI bundles `localcode-gui.exe`, which is CGo and cannot be
cross-compiled from macOS. Get a build from CI once main has the commit:

Dispatch it with the version you are releasing rather than reusing the last
push build. That binary is stamped with `-X main.version`, and it is the one
the GUI header and `localcode-gui.exe version` report; a run off `main` only
knows the *previous* tag, so it would ship a release whose desktop build
names the version before it.

```bash
gh workflow run gui-windows.yml --ref main -f version=x.y.z
gh run list --workflow=gui-windows.yml --limit 1 --json databaseId,status,conclusion,headSha
gh run download <run-id> -n localcode-gui-windows-amd64 -D /tmp/gui-exe
```

```bash
make dist VERSION=x.y.z GUI_EXE=/tmp/gui-exe/localcode-gui.exe  # runs the preflight first; refuses if docs are stale
gh release create vx.y.z \
  dist/windows/localcode-x.y.z-windows-amd64.msi \
  dist/windows/localcode-x.y.z-windows-amd64.zip \
  dist/windows/localcode-x.y.z-windows-arm64.zip \
  dist/mac/LocalCode-x.y.z-darwin-universal.tar.gz \
  --repo dennis2lee/localcode --title "vx.y.z" --notes "..."
rm -rf dist
```
