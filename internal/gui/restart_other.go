//go:build gui && !windows

package gui

// registerRestart does nothing off Windows. The macOS window is a .app
// bundle that an update replaces whole, and the install path there does
// not close the running program.
func registerRestart() {}

// InstallerRestarts is false off Windows: no installer closes the window.
func InstallerRestarts() bool { return false }
