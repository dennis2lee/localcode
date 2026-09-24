# MSI install fix (branch fix/msi-install)

## Summary

The settings window install button ran msiexec while localcode still held files under the install directory. The Restart Manager could not close the window. It showed a files-in-use dialog instead. The old product was removed before the new one installed. A failed install left neither version on the machine.

This change stages a small helper beside the downloaded MSI. The helper waits for localcode to exit. Then it runs msiexec. Then it starts localcode-gui.exe again when the parent was the desktop window. It records the result for the update check to report.

## Changes

* `internal/update/msi_status.go` (new, platform independent): msiexec exit code classification, install record read and write, helper file names, reply detail builders.
* `internal/update/install.go`: the Windows MSI branch stages the helper. It no longer starts msiexec directly.
* `internal/update/install_windows.go`: helper staging, hidden detached spawn, wait for the parent, run msiexec with a log, record the result, relaunch the window.
* `cmd/localcode/main.go`: helper mode entry through an environment variable, read before the window opens.
* `internal/daemon/update.go`, `internal/daemon/selfupdate.go`: new reply text per mode. The Restart Manager sentence is gone. `GET /api/update` reports a failed install.
* `internal/gui/restart_windows.go`, `internal/gui/restart_other.go`: removed. The Restart Manager never closes the window that asked for the install. Nothing else used the registration.
* `cmd/localcode/handoff.go`, `cmd/localcode/modes.go`: removed `envInstallerRestarts` plumbing.
* `internal/daemon/daemon.go`: removed `InstallerRestarts`. Added `DesktopWindow`.
* `internal/daemon/static/js/settings.js`: new confirm text, new started text, failed install line.
* `build/package-msi.sh`, `build/localcode.wxs`: late removal of the old product, with build checks.
* `docs/USAGE.md`, `docs/CHANGELOG.md`: updated.

## Why

* The installer must not run while the process that asked for it still holds files. Waiting inside localcode deadlocks. A helper that outlives its parent is the only ordering that works.
* Early removal (RemoveExistingProducts at sequence 1401) commits the old product removal before the new product installs. Late removal keeps the old product until the new files land. A cancelled install then leaves the old version in place.
* The bootstrapper file was skipped as unchanged and then deleted with the old product. Late removal keeps the old copy until the new transaction decides. Reasoning is below.
* The old reply promised a Restart Manager restart that never happened. The new reply says what actually happens.

## Tests

* `internal/update`: `msi_status_test.go` (exit codes, meanings, names, version parse, record and pending round trips, report decision, exact reply text, pending refusal). All pass on macOS. The panel line itself is rendered and pinned in the Web UI tests, following the repo pattern of structured daemon data with client rendering. An earlier Go-side line builder was deleted for exactly that reason: tested but never called in production is the shape the deadcode gate exists to catch.
* `internal/update/install_flags_test.go`: updated for `installerArgs(msi, log)` (`/i`, `/qb`, `/l*v` with the log last, quiet and restart-flag bans). The AST call-site guard now reads `msi_helper_windows.go` and only checks the `msiexec` call. Passes on macOS.
* `internal/daemon/update_test.go`: `TestTheCheckReportsAFailedInstall` (version, exit code, meaning, log in `last_install`) and `TestTheCheckSaysNothingAboutTheRunningInstall` (omitted and cleared). They need loopback binds, which this sandbox forbids. The same tests fail here for pre-existing cases too (for example `TestTheUpdateCheckReportsANewerRelease`). The underlying logic was verified here with a temporary non-network test against `lastInstallReport`, which passed and was then deleted.
* Web UI: `test/webui/update.test.js` grew 5 tests (confirm wording, window reply, terminal reply, failed-install line beside the offer, failed-install line when up to date). Full suite: 592 pass, 0 fail. `test/webui/harness.js` records confirm messages.
* `build/msi-checks-test.sh`: 3 fixture cases (late passes, early fails, missing InstallExecute fails). Passes. Also run against real `msiinfo` dumps of a scratch wixl MSI before and after the post-processing SQL: the check fails before and passes after.
* Mutation check: each new branch was broken and a test failed. Changed reply text, exit-code status, report decision, `/l*v` flag, settings.js line. Reverted after.
* `make check`: exit code recorded below.

Windows-only tests (run in Windows CI, in the packages `.github/workflows/gui-windows.yml` names):

* `internal/update/msi_helper_windows_test.go`: helper waits, runs a stand-in installer, records the result, relaunches the window with the parent arguments. Failed install still relaunches. Missing window binary shows a message box with the exit code and log. Terminal parent starts nothing. Second install while busy is refused with the pending version and starts no second helper. Stale copy is replaced. Helper mode is unset so children do not inherit it.
* Nothing in `cmd/localcode` needed a Windows-only test: the helper entry in `main.go` delegates to `update.RunMSIHelper`, which the tests above cover.

## Could not test

* A real MSI upgrade on Windows. No Windows machine here. The post-processing SQL was verified on a scratch wixl MSI (sequence moves as intended, checker fails before and passes after). The component GUID derivation was not diffed across real 0.147.0 and 0.148.0 MSIs; the change touches no component, key path, or file name, and the build now checks the three component names.
* The files-in-use dialog being gone. Same reason.
* The daemon install and check handlers end to end (they need loopback HTTP, forbidden in this sandbox). Covered by committed tests that run in CI.
* `make check` network lanes fail in this sandbox for pre-existing tests too. Recorded below.

## Decisions

* Helper identity travels in an environment variable, not a flag. A flag would appear in help and in the flag roster tests. The handoff successor already uses this shape (`LOCALCODE_TAKEOVER`).
* The helper is a copy of the running executable under `%LocalAppData%\localcode\updates`. It holds no file under the install directory.
* The wait has no timeout. The person decides when localcode closes.
* The log is beside the MSI and named after the version: `localcode-<version>-msi.log`.
* The record is `last-install.json` in the updates directory. It holds the version, msiexec exit code, log path and time.
* The record is cleared when a new install is staged (superseded) and when `GET /api/update` sees a successful install of the running version (lazy cleanup). Rationale: a record for the running version says nothing. A record for anything else is either a failure to report or an install the person has not relaunched into yet.
* GUI-ness comes from the running executable name (`localcode-gui.exe`). The window always runs in that binary. The console binary never opens a window. `runGUI` also sets `Daemon.DesktopWindow` for the reply text, so tests do not depend on the test binary name.
* `registerRestart`, `InstallerRestarts`, `envInstallerRestarts` and `installerRestartsNote` were removed. Rationale: after this change the Restart Manager never closes the window that asked for the install. The window closes itself. Nothing else read the registration. The two `internal/gui/restart_*.go` files are deleted, `gui.Launch` no longer registers, and `Daemon.InstallerRestarts` is replaced by `Daemon.DesktopWindow` (set only by `runGUI`). The helper unsets its own mode variables on entry, so `msiexec` and the relaunched window cannot inherit them and mistake themselves for the helper.
* The nine helper-only symbols in `msi_status.go` are allowlisted in `scripts/deadcode.allow` with reasons, following the existing `installerArgs` precedent. They are reachable only from the Windows helper file, which the macOS deadcode run never compiles.
* The `checked: false` check response carries no `last_install`. Rationale: that response means the check itself failed, and the panel shows the error. The record is reported on every successful check.
* `/update` in a terminal says "installer staged", not "installed", when the helper path is taken. Rationale: nothing is installed yet. That path needs no handoff and no daemon event: the reply is already shown in the terminal.
* The terminal is never relaunched. A new console is not the person's terminal.
* `installerArgs` now takes the MSI path and the log path. It stays platform independent and tested off Windows.
* Late removal reasoning: unversioned Go binaries, bootstrapper, PATH entry, shortcuts, `AllowDowngrades`. See below.

## Late removal reasoning

* Unversioned Go binaries: file versioning compares modified and created dates for unversioned files. Late removal keeps both copies present during the transaction, so the new files overwrite under component rules instead of landing on an emptied directory. (To be confirmed against the built MSI.)
* Bootstrapper: with early removal the new product skipped the same-version file and the old product deletion removed it. With late removal the old copy is still present while the new product decides, and the component stays installed across the upgrade.
* PATH entry in the Environment table: a per-machine environment change is reference counted across the transaction rather than removed and re-added. No action needed.
* Shortcuts: keyed off HKLM registry values, unchanged by sequencing. They resolve to INSTALLDIR in both products. Late removal does not orphan them.
* `AllowDowngrades`: kept. Late removal does not change downgrade permission. A downgrade runs the same transaction in reverse.

## make check

* Exit code: 2. The `race` and `plain` lanes fail because this sandbox forbids loopback binds: every failure is the identical `httptest: failed to listen on a port: bind: operation not permitted` panic, in packages this change does not touch (`internal/agent`, `internal/client`, `internal/mcp`, `internal/provider`, and others). Pre-existing tests fail the same way here. All other lanes pass: `vet`, `gui`, `windows`, `linux`, `deadcode`, `js` (592 Web UI tests), `docs`, `fmt`, `whitespace`.
* The `windows` lane passes, so the Windows-only files compile. The Windows-only tests themselves run in Windows CI, not here.
