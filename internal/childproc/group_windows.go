//go:build windows

package childproc

import (
	"os/exec"
	"strconv"
	"syscall"
)

// createNewProcessGroup starts the child as the root of a new process
// group. On its own this only affects console signal delivery — Windows
// has no "kill the group" call — but it keeps a Ctrl-C in the terminal
// localcode was started from out of the shell commands the model runs.
const createNewProcessGroup = 0x00000200

// NewGroup starts the child as its own process-group root.
func NewGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNewProcessGroup
}

// KillGroup kills the child and every process it started.
//
// taskkill /T walks the process tree, which is the part that matters:
// killing the shell alone leaves whatever it launched — a dev server, a
// test runner — running with the pipe still open, and the read of that
// pipe is what a cancelled tool call is blocked on.
//
// Windows has no signal to address a process group with, and a Job Object
// would have to be created before the process starts and held for its
// lifetime; taskkill is the same thing done by a tool that already ships
// with the OS.
func KillGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	Hide(kill)
	if err := kill.Run(); err != nil {
		// taskkill missing, or the tree already gone. Killing just the
		// child is strictly better than doing nothing.
		return cmd.Process.Kill()
	}
	return nil
}
