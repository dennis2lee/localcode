//go:build windows

package main

import "fmt"

// selfRestartAvailable is false here, and that decides more than the
// wording of one reply: it is why localcode does not update itself at
// startup on Windows.
//
// The Windows update is an MSI, and applying one runs msiexec, which asks
// for elevation with a dialog. Doing that unasked, before the program has
// drawn anything, would turn every startup after a release into a UAC
// prompt for something nobody initiated — and at the end of it localcode
// still could not come back into the terminal it was started from. The
// settings window's button stays the way Windows updates, because there a
// person clicked something.
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
