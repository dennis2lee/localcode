//go:build !gui

package gui

import (
	"errors"
	"net/http"
	"runtime"
)

// Launch is what a build compiled without the "gui" tag gets. The real
// implementation (gui.go) links a native webview through CGo, which the
// default pure-Go, cross-compiled release builds omit — so this returns a
// clear error telling you what to do instead of failing to compile the
// whole binary.
func Launch(title, version string, start func(progress func(string), setVersion func(string), reload func()) (http.Handler, error)) error {
	return errors.New(unavailable(runtime.GOOS))
}

// InstallerRestarts is false without a window: there is nothing for an
// installer to bring back.
func InstallerRestarts() bool { return false }

// unavailable is the explanation, per platform, because the answer is a
// different one on each and the wrong answer is worse than none.
//
// On Linux there is no window to have: the webview links WebKitGTK
// through CGo, which is a build per distribution rather than a build
// flag. Telling somebody there to "build with -tags gui on macOS or
// Windows" — which is what this said until it was reported from an
// Ubuntu install — names two operating systems they are not using and
// leaves out the thing they actually want, which is the same interface
// in a browser tab.
func unavailable(goos string) string {
	if goos == "linux" {
		return "there is no desktop window on Linux, because the native webview links WebKitGTK through CGo, which means a build per distribution. " +
			"Run localcode without --gui and open the Web UI in a browser (http://127.0.0.1:4096 unless --listen says otherwise) — it is the same interface the window shows"
	}
	return "this build has no desktop window: it was compiled without -tags gui. " +
		"The macOS .app and the Windows .msi on the releases page have it, or build one with: go build -tags gui -o localcode-gui ./cmd/localcode. " +
		"Without it, run localcode and open the Web UI in a browser (http://127.0.0.1:4096 unless --listen says otherwise)"
}

// Available reports whether this build can open a window.
func Available() bool { return false }
