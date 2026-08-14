//go:build gui && !darwin

package gui

// hideTitleBar does nothing away from macOS.
//
// On Windows the caption can be removed — SetWindowLongPtr, minus
// WS_CAPTION — and doing only that leaves a window that cannot be moved,
// resized from the top, minimised or closed: those are all handled by the
// non-client area that was just deleted. Making it work means subclassing
// the window procedure to answer WM_NCCALCSIZE and WM_NCHITTEST, and
// drawing the buttons in the page. That is a real feature, not a flag, and
// it is one that would ship from here unverified — this machine cannot run
// it. Until it can be tried on Windows, the title bar stays.
func hideTitleBar(uintptr) {}
