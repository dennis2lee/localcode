package dialog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// The tests below exercise the folder-picker cancellation paths without
// opening a real OS dialog.
//
// TestPickDirectoryContextCancel in dialog_test.go is opt-in for a reason:
// it drives the real picker and puts a window on screen, so it only runs
// with LOCALCODE_DIALOG_TEST=1 and a human nearby to close the window. But
// the cancellation contract it guards has two halves, and only one of them
// needs the window:
//
//   - the context half (CommandContext kills the helper, the call comes
//     back instead of blocking until a human acts) needs only a helper
//     that outlives the context;
//   - the mapping half (a helper that reports "cancelled" becomes
//     ErrCancelled, while a helper that reports failure stays an error)
//     needs only a helper whose exit status and stderr look like the real
//     one's.
//
// Both halves run here with stand-in helpers: small shell scripts named
// exactly what the picker would execute (osascript, zenity), placed first
// on PATH. Everything above the exec boundary — argument construction,
// process teardown, exit-status mapping, output cleanup — is the real
// production code. What is faked is the OS on the other side of it, which
// is the half no test suite can summon on demand.

// requireShellHelper skips on platforms where the stand-in helpers cannot
// run. They are POSIX shell scripts executed by name through PATH, which
// Windows does not do: exec there needs an extension it knows. The picker
// paths under test still run on both CI legs (macOS and Linux), which is
// where the gate runs them; the Windows picker path (pickWindows) cannot
// be faked this way at all, since it shells out to the real PowerShell,
// so it stays manual-only. See the report for why.
func requireShellHelper(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stand-in helpers are POSIX shell scripts, which Windows does not execute by name")
	}
}

// installHelper writes a stand-in executable called name into a directory
// that comes first on PATH, so the picker's exec finds it instead of the
// real OS helper. It stays in effect for the calling test only.
func installHelper(t *testing.T, name, body string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write stand-in %s: %v", name, err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A cancelled macOS dialog is osascript exiting 1 with "User canceled" on
// stderr — the same exit a script error produces, which is why the message
// is the whole distinction. The stand-in reports exactly that.
func TestACancelledDarwinDialogReportsCancellation(t *testing.T) {
	requireShellHelper(t)
	installHelper(t, "osascript", "#!/bin/sh\necho '0:0: execution error: User canceled. (-128)' >&2\nexit 1\n")

	if _, err := pickDarwin(context.Background(), "choose", ""); !errors.Is(err, ErrCancelled) {
		t.Errorf("a cancelled picker returned %v, want ErrCancelled", err)
	}
}

// The companion to the test above, and the reason the mapping keys on the
// message rather than the exit status: osascript also exits 1 for a script
// that failed, and reporting that as a cancellation would tell the caller
// "the user changed their mind" about a dialog that errored. A picker
// that broke must stay an error.
func TestAFailingDarwinDialogIsNotACancellation(t *testing.T) {
	requireShellHelper(t)
	installHelper(t, "osascript", "#!/bin/sh\necho '0:0: execution error: No such file or directory. (2)' >&2\nexit 1\n")

	_, err := pickDarwin(context.Background(), "choose", "")
	if err == nil {
		t.Fatal("a failing picker returned no error at all")
	}
	if errors.Is(err, ErrCancelled) {
		t.Errorf("a failing picker returned ErrCancelled (%v): a broken dialog reads as a dismissed one", err)
	}
}

// The success path through the same stand-in, proving the tests above run
// the real picker code rather than passing against a helper nothing calls:
// if PATH shadowing broke and the real osascript ran, this would block on
// a window (or fail), not return the stand-in's path.
func TestAChosenDarwinDirectoryReturnsThePath(t *testing.T) {
	requireShellHelper(t)
	installHelper(t, "osascript", "#!/bin/sh\nprintf '/tmp/chosen/\\n'\nexit 0\n")

	got, err := pickDarwin(context.Background(), "choose", "")
	if err != nil {
		t.Fatalf("a successful pick returned %v", err)
	}
	if got != "/tmp/chosen" {
		t.Errorf("a successful pick returned %q, want the trailing slash cleaned to /tmp/chosen", got)
	}
}

// Cancelling on Linux is any nonzero exit from the helper: zenity and
// kdialog both use the exit status alone, with no message to read.
func TestACancelledLinuxDialogReportsCancellation(t *testing.T) {
	requireShellHelper(t)
	installHelper(t, "zenity", "#!/bin/sh\nexit 1\n")

	if _, err := pickLinux(context.Background(), "choose", ""); !errors.Is(err, ErrCancelled) {
		t.Errorf("a cancelled picker returned %v, want ErrCancelled", err)
	}
}

// The success path through the same stand-in, for the same reason as the
// Darwin one: it proves the cancellation test above exercises the real
// picker and not a helper nothing invokes.
func TestAChosenLinuxDirectoryReturnsThePath(t *testing.T) {
	requireShellHelper(t)
	installHelper(t, "zenity", "#!/bin/sh\nprintf '/tmp/picked\\n'\nexit 0\n")

	got, err := pickLinux(context.Background(), "choose", "")
	if err != nil {
		t.Fatalf("a successful pick returned %v", err)
	}
	if got != "/tmp/picked" {
		t.Errorf("a successful pick returned %q, want /tmp/picked", got)
	}
}

// The context half of the contract, on the Darwin side: a helper that
// never exits on its own must still come back once the context is gone.
// exec on macOS runs the stand-in (exec replaces the shell with sleep, so
// the kill lands on the sleeper itself and leaves nothing behind); the
// requirement is only that the call returns promptly with an error. What
// it must NOT do is sit there until a human closes something, which is
// the failure TestPickDirectoryContextCancel guards with a real window.
func TestACancelledContextStopsTheDarwinHelper(t *testing.T) {
	requireShellHelper(t)
	installHelper(t, "osascript", "#!/bin/sh\nexec sleep 30\n")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, err := pickDarwin(ctx, "test: this should close itself", "")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a killed picker returned a path, but nobody chose one")
	}
	if elapsed > 10*time.Second {
		t.Errorf("the picker took %v to honour a 1s context; it outlived the helper it should have killed", elapsed)
	}
}

// The same teardown on the Linux side, where a killed helper is an
// ExitError like a Cancel click, so the documented mapping reports it as
// ErrCancelled rather than as a failure.
func TestACancelledContextStopsTheLinuxHelper(t *testing.T) {
	requireShellHelper(t)
	installHelper(t, "zenity", "#!/bin/sh\nexec sleep 30\n")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, err := pickLinux(ctx, "test: this should close itself", "")
	elapsed := time.Since(start)
	if !errors.Is(err, ErrCancelled) {
		t.Errorf("a killed picker returned %v, want ErrCancelled", err)
	}
	if elapsed > 10*time.Second {
		t.Errorf("the picker took %v to honour a 1s context; it outlived the helper it should have killed", elapsed)
	}
}
