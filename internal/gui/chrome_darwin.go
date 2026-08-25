//go:build gui && darwin

package gui

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

// lcHideTitleBar takes the title bar out of the window's appearance
// without taking it out of the window.
//
// Three changes, and the third is what makes the first two work: the
// title text is hidden, the bar is drawn transparent so the window's own
// background shows through it, and that background is set to the page's
// chrome colour. The result is one continuous surface from the top of the
// window down, with the close/minimise/zoom buttons floating on it.
//
// The bar is deliberately still there. A window with NSWindowStyleMask
// FullSizeContentView puts the web content under those buttons, where the
// top-left of the page — the session panel's own controls — would sit
// behind them; and a window with the title bar genuinely removed cannot
// be moved or closed at all unless the page grows its own drag region and
// its own buttons, which is a lot of machinery to arrive back where the OS
// already was.
static void lcHideTitleBar(void *ptr) {
  NSWindow *w = (__bridge NSWindow *)ptr;
  if (!w) return;
  w.titlebarAppearsTransparent = YES;
  w.titleVisibility = NSWindowTitleHidden;
  // --bg from the Web UI's stylesheet (rgb 19,20,23). Kept in step by
  // chrome_test.go, which reads the value out of style.css.
  w.backgroundColor = [NSColor colorWithSRGBRed:(19/255.0) green:(20/255.0) blue:(23/255.0) alpha:1.0];
}
*/
import "C"

import "unsafe"

// windowControls reports that the page draws no buttons of its own: the
// macOS window keeps its real title bar, and with it the traffic lights.
func windowControls(uintptr) func(string) { return nil }

// hideTitleBar blends the title bar into the window on macOS.
func hideTitleBar(win uintptr) {
	if win == 0 {
		return
	}
	C.lcHideTitleBar(unsafe.Pointer(win))
}
