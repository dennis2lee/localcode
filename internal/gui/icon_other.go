//go:build gui && !windows

package gui

// setWindowIcon does nothing away from Windows.
//
// macOS takes a window's icon from the .app bundle it is running out of
// (see build/package-mac-gui.sh), not from the window, so there is
// nothing for a running process to set. A bare binary run outside a
// bundle gets the generic one, which is the honest thing to show for a
// program that has not been installed.
func setWindowIcon(uintptr) {}
