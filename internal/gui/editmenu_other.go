//go:build gui && !darwin

package gui

// installEditMenu is nothing off macOS. The Command key equivalents it
// installs are a macOS convention; the Windows window and browsers provide
// their own editing bindings without one.
func installEditMenu() {}
