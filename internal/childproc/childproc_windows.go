//go:build windows

package childproc

import (
	"os/exec"
	"syscall"
)

// createNoWindow is Windows' CREATE_NO_WINDOW process creation flag. It runs
// a console application with a console that has no window, which is what
// stops each child from flashing (or leaving) a black box on screen.
//
// It is ignored if CREATE_NEW_CONSOLE or DETACHED_PROCESS is also set; we
// set neither.
const createNoWindow = 0x08000000

// HideConsole suppresses a console window for the child without touching
// how its own windows are shown — for the handful of children that are GUI
// programs we are starting *because* we want their window.
//
// Hide's belt-and-braces STARTF_USESHOWWINDOW + SW_HIDE is inherited by the
// first top-level window a GUI child creates, so using Hide on
// explorer.exe opened the folder window hidden: no window, no error,
// nothing to distinguish it from a button that does nothing.
func HideConsole(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}

// Hide keeps a child process from opening a console window. See the package
// comment for why every child needs this.
func Hide(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	HideConsole(cmd)
	// Belt and braces: STARTF_USESHOWWINDOW + SW_HIDE, for the case of a
	// child that creates a window of its own rather than inheriting one.
	cmd.SysProcAttr.HideWindow = true
}
