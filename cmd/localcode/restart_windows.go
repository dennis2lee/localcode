//go:build windows

package main

import "fmt"

// execSelf is never reached on Windows.
//
// There is no exec: a Windows process cannot replace its own image, and
// the alternative — spawn a copy and exit — hands the terminal back to
// the shell before the new one has drawn anything, which for a TUI is a
// worse answer than not restarting.
//
// It costs nothing either, because the case does not arise: the Windows
// update is an MSI, msiexec cannot replace files localcode is holding
// open, and so localcode has to exit for the install to happen at all.
// The restart there is the user starting the program again, which they
// are already doing.
func execSelf() error {
	return fmt.Errorf("localcode cannot restart itself on Windows")
}
