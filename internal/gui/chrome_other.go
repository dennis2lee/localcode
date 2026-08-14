//go:build gui && !darwin && !windows

package gui

// hideTitleBar does nothing on the platforms that have neither
// implementation — macOS is in chrome_darwin.go, Windows in
// chrome_windows.go, and everything else keeps whatever frame its window
// manager draws.
func hideTitleBar(uintptr) {}

// windowControls reports that the page has no title bar of its own to
// draw here.
func windowControls(uintptr) func(string) { return nil }
