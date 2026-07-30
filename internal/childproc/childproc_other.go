//go:build !windows

package childproc

import "os/exec"

// Hide does nothing outside Windows: a spawned process there inherits the
// parent's stdio and never gets a terminal window of its own, so there is
// nothing to suppress.
func Hide(cmd *exec.Cmd) {}
