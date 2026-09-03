//go:build windows

package main

import "fmt"

// selfRestartAvailable is false here: a Windows process cannot replace
// its own image, so the exec that brings a Unix localcode up on a new
// binary at startup has no equivalent.
//
// It used to decide more than that — it was why localcode did not update
// itself at startup on Windows at all, since the alternative was the MSI,
// a UAC prompt, and a console that could not be returned to. That is no
// longer the alternative. Where exec is not available the new version is
// started beside this process on the listener it already holds, before
// anything is served, and this process runs the TUI, holds the console,
// or fronts the window against it. See startuphandoff.go.
const selfRestartAvailable = false

// execSelf is never reached on Windows.
//
// There is no exec: a Windows process cannot replace its own image, and
// the alternative — spawn a copy and exit — hands the terminal back to
// the shell before the new one has drawn anything, which for a TUI is a
// worse answer than not restarting.
//
// It costs nothing either, because there is nothing better available. The
// Windows update is an MSI, and the Restart Manager is what closes
// localcode so the files can be replaced (see
// internal/update/install_windows.go). The Restart Manager can also put an
// application back, but it documents that a CONSOLE application is
// restarted in a new console, which is not the terminal the person is
// sitting in front of. A custom action in the package and a detached
// helper process both reach the same place for the same reason: nothing
// outside a terminal can hand a process back into it.
//
// So the terminal update ends with localcode closed and the person
// starting it again, and the panel says so rather than promising a
// restart it cannot perform.
func execSelf() error {
	return fmt.Errorf("localcode cannot restart itself on Windows")
}
