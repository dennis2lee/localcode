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
func startInstaller(path string) error {
	cmd := exec.Command("msiexec", "/i", path)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the installer: %w", err)
	}
	// Released rather than waited for: this process is about to exit, and a
	// child that outlives it is the entire point here.
	return cmd.Process.Release()
}
