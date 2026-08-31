//go:build windows

package update

import (
	"fmt"
	"os/exec"
)

// startInstaller hands the MSI to Windows and returns without waiting.
//
// Not waiting is deliberate: msiexec asks for elevation, shows a dialog the
// user works through at their own pace, and cannot finish until localcode
// has exited and let go of its files. Waiting for it here would deadlock
// the two of them — the installer waiting for the files, localcode waiting
// for the installer.
//
// `msiexec /i` rather than opening the file: the MSI is the same package
// the installed version came from, so this is an upgrade in place, and
// running it through msiexec is what makes it one. No /quiet — replacing
// the program someone is using is not something to do behind a progress bar
// they cannot see or cancel. Deliberately not hidden the way localcode's
// other children are (see internal/childproc): this one is meant to be seen.
//
// /qb, and it is the difference between an update that applies and one
// that does not.
//
// Windows Installer asks the Restart Manager which processes are holding
// the files it is about to replace, and then offers to close them. Which
// offer it makes depends on the UI level. At full UI it looks for an
// authored MsiRMFilesInUse dialog, and this package has none: wixl cannot
// author dialogs at all, so the built MSI has no Dialog, Control or
// ControlEvent table, and the documented fallback for a missing dialog is
// that no message is shown and the install continues with a reboot
// scheduled instead. So the old command left localcode running, the files
// unreplaced, and a pending reboot nobody was told about: an update that
// reported success and changed nothing until the machine was restarted.
//
// Basic UI has a built-in files-in-use dialog that needs no authoring. The
// Restart Manager closes localcode, which for a console program is a
// CTRL_C_EVENT that Go delivers as SIGINT and the TUI shuts down on with
// the terminal restored, and the install completes.
//
// What this does not do is bring localcode back, and no flag can. The
// Restart Manager restarts a console application in a NEW console, which
// is not the terminal the person is sitting in front of. See
// cmd/localcode/restart_windows.go.
func startInstaller(path string) error {
	cmd := exec.Command("msiexec", installerArgs(path)...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the installer: %w", err)
	}
	// Released rather than waited for: this process is about to exit, and a
	// child that outlives it is the entire point here.
	return cmd.Process.Release()
}
