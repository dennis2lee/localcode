//go:build !gui

package gui

import (
	"errors"
	"net/http"
)

// Launch is what a build compiled without the "gui" tag gets. The real
// implementation (gui.go) links a native webview through CGo, which the
// default pure-Go, cross-compiled release builds omit — so this returns a
// clear error telling you how to get a GUI build rather than failing to
// compile the whole binary.
func Launch(title string, handler http.Handler) error {
	return errors.New("this build has no GUI support; build with -tags gui on macOS or Windows (needs CGo and a native webview)")
}

// Available reports whether this build can open a window.
func Available() bool { return false }
