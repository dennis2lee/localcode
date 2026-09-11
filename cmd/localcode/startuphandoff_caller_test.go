package main

import (
	"os"
	"strings"
	"testing"
)

// Who decides that a startup handoff is needed.
//
// It used to be the platform: selfRestartAvailable, true on macOS and
// Linux, false on Windows. That is right for a headless daemon and wrong
// for the desktop window, which cannot exec on any platform because it is
// holding a native window — and runGUI has never called
// autoUpdateAtStartup either, so on a Mac or a Linux desktop the window
// simply never updated itself at startup. Neither path ran.
//
// Now the caller answers. A window says "I cannot exec" whatever the
// platform allows, and takes the successor path every headless daemon
// already takes on Windows.
//
// Read off the source rather than driven, because the function's next
// step is to ask GitHub for a release and install it — a test that ran it
// would be a test that downloads. What is worth protecting here is which
// answer each caller gives, and that is exactly what is written down.

func modesSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("modes.go")
	if err != nil {
		t.Fatalf("read modes.go: %v", err)
	}
	return string(b)
}

func TestTheWindowSaysItCannotExec(t *testing.T) {
	src := modesSource(t)
	if !strings.Contains(src, "startupHandoffBinary(d, os.Stderr, false)") {
		t.Error("runGUI no longer says it cannot exec; on macOS and Linux that is a window with no startup update at all")
	}
}

// The other two still ask the platform, which is right for them: a
// headless daemon and a terminal can both be replaced by an exec, and on
// the platforms where they can, that is the cheaper path.
func TestTheHeadlessCallersStillAskThePlatform(t *testing.T) {
	src := modesSource(t)
	if n := strings.Count(src, "startupHandoffBinary(d, os.Stderr, selfRestartAvailable)"); n != 2 {
		t.Errorf("%d callers ask the platform, want the headless daemon and the terminal", n)
	}
}

// And nothing reads the platform constant in place of an answer any
// more, which is the shape of the fault: one caller's truth applied to
// every caller.
func TestTheHandoffNoLongerDecidesForItself(t *testing.T) {
	b, err := os.ReadFile("startuphandoff.go")
	if err != nil {
		t.Fatalf("read startuphandoff.go: %v", err)
	}
	body := string(b)
	if !strings.Contains(body, "func startupHandoffBinary(d *daemon.Daemon, out io.Writer, canExec bool)") {
		t.Fatal("startupHandoffBinary no longer takes the caller's answer")
	}
	if strings.Contains(body, "if selfRestartAvailable ||") {
		t.Error("it still decides from the platform, which is what left the window without a startup update")
	}
}

// runGUI must not grow a call to the exec path either. It is the one
// caller that cannot survive one, and the comment on autoUpdateAtStartup
// is the only thing standing between a future edit and a window that
// replaces itself mid-frame.
func TestTheWindowNeverExecsAtStartup(t *testing.T) {
	src := modesSource(t)
	start := strings.Index(src, "func runGUI(")
	if start < 0 {
		t.Fatal("runGUI is gone")
	}
	end := strings.Index(src[start+1:], "\nfunc ")
	if end < 0 {
		end = len(src) - start - 1
	}
	if strings.Contains(src[start:start+end], "autoUpdateAtStartup(") {
		t.Error("runGUI calls autoUpdateAtStartup, which ends in exec while this process holds a native window")
	}
}
