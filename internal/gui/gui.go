//go:build gui

// Package gui opens a native desktop window rendering the same Web UI the
// daemon already serves, so localcode can be a single double-clickable app
// instead of "start a server, then open a browser".
//
// It is behind the "gui" build tag on purpose. The window is a native OS
// webview (WKWebView on macOS, WebView2 on Windows) reached through CGo,
// which the default pure-Go builds — the ones the release pipeline
// cross-compiles from one machine — deliberately leave out. Only a build
// made with `-tags gui`, on the target OS itself, links it. See stub.go
// for what a non-gui build gets instead.
package gui

import (
	"fmt"
	"net"
	"net/http"

	webview "github.com/webview/webview_go"
)

// Launch serves handler on a fresh loopback port and opens a native window
// pointed at it, blocking until the window closes. The daemon is the same
// one the TUI and browser talk to; this is just another local client, in a
// window we own.
func Launch(title string, handler http.Handler) error {
	// Port 0 lets the OS pick a free port — no fixed 4096 to collide with a
	// separately running daemon, and loopback-only so nothing is exposed off
	// the machine.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("bind loopback port: %w", err)
	}
	go func() { _ = http.Serve(ln, handler) }()

	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle(title)
	w.Navigate("http://" + ln.Addr().String())

	// The window size is applied from inside the message loop, not before
	// Run(), and that ordering is the fix for a Windows startup bug: the
	// window opened with everything drawn far too large, and stayed that
	// way until the user resized it by hand, at which point it snapped to
	// the right size.
	//
	// The cause is that WebView2 latches a rasterization scale when its
	// controller is created — which webview_go does inside New(), while the
	// window is still at its built-in 640x480 default — and the library
	// never calls put_RasterizationScale or enables monitor-scale
	// detection. On a laptop running at 150% or 200% display scaling the
	// latched scale is wrong, and the only thing that makes WebView2
	// recompute it is a WM_SIZE reaching the widget. Calling SetSize before
	// Run() does resize the window, but it happens before the loop is
	// pumping, so the resulting re-layout uses the stale scale; the first
	// manual drag is what finally corrects it.
	//
	// Dispatch runs this on the UI thread once the loop is live, so the
	// 640x480 -> 1100x800 change is a real resize that WebView2 sees and
	// responds to. Not a no-op guard: the size genuinely differs from the
	// default, which is why it must NOT also be set before Run() — with
	// both, the dispatched call would be a same-size SetWindowPos and
	// might not produce a WM_SIZE at all.
	//
	// macOS is unaffected (WKWebView takes its backing scale from the
	// screen), and the dispatched resize is harmless there.
	w.Dispatch(func() {
		w.SetSize(1100, 800, webview.HintNone)
	})

	w.Run() // blocks until the window is closed
	return nil
}

// Available reports whether this build can open a window.
func Available() bool { return true }
