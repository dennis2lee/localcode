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
	w.SetSize(1100, 800, webview.HintNone)
	w.Navigate("http://" + ln.Addr().String())
	w.Run() // blocks until the window is closed
	return nil
}

// Available reports whether this build can open a window.
func Available() bool { return true }
