//go:build !windows

package childproc

import "os/exec"

// Hide does nothing outside Windows: a spawned process there inherits the
// parent's stdio and never gets a terminal window of its own, so there is
// nothing to suppress.
func Hide(cmd *exec.Cmd) {}

// HideConsole likewise does nothing outside Windows. See the Windows file
// for what it is and why it is not the same as Hide.
func HideConsole(cmd *exec.Cmd) {}
