//go:build !windows

package childproc

import (
	"os/exec"
	"syscall"
)

// NewGroup puts the child in a process group of its own, so KillGroup can
// take down everything it starts and not just the child itself.
func NewGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// KillGroup kills the child and every process it started.
//
// The child is the group leader (NewGroup made it one), so its pid is also
// the group id, and a negative pid is how a signal is addressed to a whole
// group.
func KillGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		// The group may already be gone, or NewGroup may never have run
		// (a caller that built the command itself). Killing just the
		// child is strictly better than doing nothing.
		return cmd.Process.Kill()
	}
	return nil
}
