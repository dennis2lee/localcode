//go:build gui && !windows

package gui

import (
	"errors"
	"os/exec"
	"runtime"

	webview "github.com/webview/webview_go"
)

// routeNewWindows gives the page lcOpenExternal off Windows.
//
// WebView2 asks the window what to do with a link that wants a window of
// its own, and newwindow_windows.go answers with the system's default
// browser. WKWebView and WebKitGTK ask nobody: with no UI delegate a
// target="_blank" link does nothing, so a link in the model's output could
// not be opened from this window at all. The page sends a click on a web
// link here instead (internal/daemon/static/js/links.js), and the address
// goes to the system the way it does on Windows, held to the same rules.
func routeNewWindows(w webview.WebView) {
	w.Bind("lcOpenExternal", openExternal)
}

// openExternal opens raw in the system's default browser, if externalURL
// lets it through: http or https, with a host, and nothing a command line
// could read as more than one argument.
func openExternal(raw string) error {
	target, reason := externalURL(raw)
	if reason != "" {
		return errors.New("not opened: " + reason)
	}
	name, args := browserCommand(runtime.GOOS, target)
	// No shell: the address is one argument, and it starts with "http", so
	// neither program can read it as an option.
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// browserCommand is the program that opens target in the default browser
// on goos: open(1) on macOS, which resolves the default the way Finder
// does, and xdg-open elsewhere, which asks the desktop the same.
func browserCommand(goos, target string) (string, []string) {
	if goos == "darwin" {
		return "open", []string{target}
	}
	return "xdg-open", []string{target}
}
