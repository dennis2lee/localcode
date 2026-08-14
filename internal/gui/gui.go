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
	"runtime"
	"time"

	webview "github.com/webview/webview_go"
)

// Launch opens the window first and does the slow work behind it.
//
// start is called on its own goroutine once the window is up. It reports
// what it is doing through the progress func it is given — each call
// replaces the splash screen's status line — and returns the handler to
// show when it is finished. Only then is a loopback server bound and the
// window pointed at it.
//
// The order is the point. Building a daemon means reading config,
// opening providers, loading every session from disk and handshaking
// with each configured MCP server, which together can run to several
// seconds. Doing that before creating the window meant several seconds
// in which the app had produced nothing at all — no window, no error,
// nothing to distinguish "working" from "failed to start". The obvious
// response is to launch it again, and then two are starting.
//
// Blocks until the window is closed. If start fails, the error is shown
// in the window (this binary has no console to print it to) and returned
// once the user closes it.
func Launch(title, version string, start func(progress func(string)) (http.Handler, error)) error {
	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle(title)
	w.Navigate(dataURL(splashHTML(version)))
	setWindowIcon(uintptr(w.Window()))
	hideTitleBar(uintptr(w.Window()))

	// Where the OS frame has been taken away, the page draws the buttons
	// that went with it and works them through here. Bound before the
	// first navigation, and re-injected by webview on every document
	// after it, which is also how the page knows to draw them at all: it
	// checks whether this function exists.
	if cmd := windowControls(uintptr(w.Window())); cmd != nil {
		w.Bind("lcWindowCommand", cmd)
	}

	// Written by the start goroutine, read after Run returns — the window
	// closing is what synchronizes them, since Run does not return until
	// the message loop is finished with the goroutine's last Dispatch.
	var startErr error

	go func() {
		progress := func(msg string) {
			w.Dispatch(func() { w.Eval(jsCall("lcStatus", msg)) })
		}
		handler, err := start(progress)
		if err != nil {
			startErr = err
			// Left on screen rather than exiting: a window that appears
			// and vanishes is indistinguishable from a crash, and the
			// message is the only copy of the reason this build produces.
			w.Dispatch(func() { w.Eval(jsCall("lcFailed", err.Error())) })
			return
		}

		// Port 0 lets the OS pick a free port — no fixed 4096 to collide
		// with a separately running daemon, and loopback-only so nothing
		// is exposed off the machine.
		ln, lerr := net.Listen("tcp", "127.0.0.1:0")
		if lerr != nil {
			startErr = fmt.Errorf("bind loopback port: %w", lerr)
			w.Dispatch(func() { w.Eval(jsCall("lcFailed", startErr.Error())) })
			return
		}
		go func() { _ = http.Serve(ln, handler) }()
		w.Dispatch(func() { w.Navigate("http://" + ln.Addr().String()) })
	}()

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
		w.SetSize(winW, winH, webview.HintNone)
	})
	nudgeScale(w)

	w.Run() // blocks until the window is closed
	return startErr
}

const (
	winW = 1100
	winH = 800
)

// nudgeScale re-sends WM_SIZE to the window a couple of times after the
// message loop has started.
//
// The dispatched SetSize above was supposed to be enough, and on some
// machines it is, but it is a race we do not control: the callback runs on
// the first turn of the loop, which can still be ahead of WebView2
// finishing its own asynchronous controller setup. Lose that race and the
// resize lands on a widget that is not yet listening, the stale scale
// survives, and the window opens with everything drawn far too large —
// exactly the bug the dispatch was added to fix, just less often. The user
// then drags the window border, WebView2 finally sees a WM_SIZE, and the
// text snaps to the right size.
//
// So instead of assuming a single well-timed resize, send more of them,
// spread out past any plausible initialization. Each one is a real size
// change (one pixel taller, then back) because a same-size SetWindowPos
// can be optimized into producing no WM_SIZE at all, which is the failure
// mode this is working around in the first place. The one-pixel bounce is
// not visible; a window that renders at 150% scale is.
//
// A cleaner fix is to call put_RasterizationScale on the WebView2
// controller, but webview_go does not expose the controller, so it is not
// reachable from here without forking the binding.
//
// Windows only: WKWebView takes its backing scale from the screen and has
// never shown this, and there is no reason to make a mac window twitch.
func nudgeScale(w webview.WebView) {
	if runtime.GOOS != "windows" {
		return
	}
	go func() {
		for _, d := range []time.Duration{300 * time.Millisecond, 1500 * time.Millisecond} {
			time.Sleep(d)
			w.Dispatch(func() {
				w.SetSize(winW, winH+1, webview.HintNone)
				w.SetSize(winW, winH, webview.HintNone)
			})
		}
	}()
}

// Available reports whether this build can open a window.
func Available() bool { return true }
