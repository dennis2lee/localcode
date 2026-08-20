//go:build !gui

package gui

import (
	"strings"
	"testing"
)

// Reported from an Ubuntu install: `localcode --gui` answered "build with
// -tags gui on macOS or Windows", which on Linux names two operating
// systems the reader is not using, describes a build that does not exist
// for them, and never mentions the Web UI — the thing that actually gives
// them the interface they asked for.
func TestTheNoWindowMessageIsUsableOnEachPlatform(t *testing.T) {
	linux := unavailable("linux")
	if strings.Contains(linux, "-tags gui") {
		t.Errorf("Linux is told to make a build that does not exist for it: %q", linux)
	}
	if !strings.Contains(linux, "http://127.0.0.1:4096") {
		t.Errorf("Linux is not told where the Web UI is: %q", linux)
	}
	if !strings.Contains(linux, "no desktop window on Linux") {
		t.Errorf("the message does not say the window is absent by design: %q", linux)
	}

	for _, goos := range []string{"darwin", "windows"} {
		msg := unavailable(goos)
		if !strings.Contains(msg, "-tags gui") {
			t.Errorf("%s is not told how to get a window: %q", goos, msg)
		}
		if !strings.Contains(msg, "http://127.0.0.1:4096") {
			t.Errorf("%s is not told what to do meanwhile: %q", goos, msg)
		}
	}
}
