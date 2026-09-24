//go:build windows

package update

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The helper waits for the parent, runs a stand-in for msiexec, and
// records the result. Every Windows-only seam is injected: the wait, the
// installer, the relaunch and the alert.
func TestTheHelperWaitsRunsAndRecords(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.exe")
	if err := os.WriteFile(src, []byte("not a real binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	msi := filepath.Join(dir, "localcode-0.46.0-windows-amd64.msi")
	if err := os.WriteFile(msi, []byte("msi"), 0o644); err != nil {
		t.Fatal(err)
	}

	var spawned struct{ helper, pending string }
	defer func(f func(string, string, windows.Handle) error) { spawnMSIHelper = f }(spawnMSIHelper)
	spawnMSIHelper = func(helper, pending string, parent windows.Handle) error {
		spawned.helper, spawned.pending = helper, pending
		return nil
	}
	parentExe := filepath.Join(dir, "localcode-gui.exe")
	if err := stageMSIInstaller(dir, src, msi, "0.46.0", parentExe, []string{"--gui"}, true); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if spawned.helper != HelperCopyPath(dir, "windows") {
		t.Errorf("spawned helper = %q", spawned.helper)
	}
	if _, err := os.Stat(spawned.helper); err != nil {
		t.Errorf("the helper copy is not on disk: %v", err)
	}

	// The helper half, with a stand-in installer.
	defer func(f func(string) error) { waitMSIParent = f }(waitMSIParent)
	waited := false
	waitMSIParent = func(raw string) error { waited = true; return nil }
	defer func(f func(string, string) int) { runMSIInstaller = f }(runMSIInstaller)
	runMSIInstaller = func(gotMSI, gotLog string) int {
		if gotMSI != msi {
			t.Errorf("installer ran on %q, want %q", gotMSI, msi)
		}
		if gotLog != MSILogPath(msi, "0.46.0") {
			t.Errorf("installer logged to %q", gotLog)
		}
		return 0
	}
	var relaunched struct {
		target string
		args   []string
	}
	defer func(f func(string, []string) error) { relaunchMSI = f }(relaunchMSI)
	relaunchMSI = func(target string, args []string) error {
		relaunched.target, relaunched.args = target, args
		return nil
	}
	alerted := false
	defer func(f func(string, string)) { alertMSI = f }(alertMSI)
	alertMSI = func(title, text string) { alerted = true }

	// The window binary is beside the parent, so it comes back with the
	// parent's arguments.
	gui := filepath.Join(dir, "localcode-gui.exe")
	if err := os.WriteFile(gui, []byte("gui"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The helper's mode must not leak into what it starts: msiexec and
	// the relaunched window inherit its environment, and either one
	// seeing the mode would mistake itself for the helper.
	t.Setenv(EnvMSIHelper, spawned.pending)
	t.Setenv(EnvMSIParent, "1")
	if err := RunMSIHelper(spawned.pending); err != nil {
		t.Fatalf("helper: %v", err)
	}
	if os.Getenv(EnvMSIHelper) != "" || os.Getenv(EnvMSIParent) != "" {
		t.Error("the helper left its mode in the environment its children inherit")
	}
	if !waited {
		t.Error("the helper did not wait for the parent")
	}
	rec, err := ReadMSIRecord(dir)
	if err != nil {
		t.Fatalf("no record: %v", err)
	}
	if rec.Version != "0.46.0" || rec.ExitCode != 0 || rec.Log != MSILogPath(msi, "0.46.0") {
		t.Errorf("record = %+v", rec)
	}
	if relaunched.target != gui {
		t.Errorf("relaunched = %q, want the window beside the parent", relaunched.target)
	}
	if len(relaunched.args) != 1 || relaunched.args[0] != "--gui" {
		t.Errorf("relaunched with %q, want the parent's arguments", relaunched.args)
	}
	if alerted {
		t.Error("a message box appeared although the window came back")
	}
}

// A failed install still brings the window back: whatever is at that
// path is what the person has. The failure is said in a message box
// first, because the window comes back over it.
func TestAFailedInstallStillRelaunches(t *testing.T) {
	dir, pending := helperFixture(t, true)
	waited, installed := stubHelperSeams(t, 1603)
	relaunched := stubRelaunch(t)
	var title, text string
	defer func(f func(string, string)) { alertMSI = f }(alertMSI)
	alertMSI = func(gotTitle, gotText string) { title, text = gotTitle, gotText }

	if err := RunMSIHelper(pending); err != nil {
		t.Fatalf("helper: %v", err)
	}
	if !*waited || !*installed {
		t.Error("the helper skipped the wait or the installer")
	}
	rec, err := ReadMSIRecord(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rec.ExitCode != 1603 {
		t.Errorf("record = %+v, want exit 1603", rec)
	}
	if title == "" || !strings.Contains(text, "1603") {
		t.Errorf("alert = %q %q, want the exit code before the window comes back", title, text)
	}
	if *relaunched == "" {
		t.Error("a failed install did not bring the window back")
	}
}

// Another installation in progress is neither installed nor cancelled,
// so it says so in a message box, and the window still comes back.
func TestABusyInstallAlertsAndRelaunches(t *testing.T) {
	_, pending := helperFixture(t, true)
	stubHelperSeams(t, 1618)
	relaunched := stubRelaunch(t)
	alerted := stubAlert(t)

	if err := RunMSIHelper(pending); err != nil {
		t.Fatalf("helper: %v", err)
	}
	if !*alerted {
		t.Error("no message box for an install refused by another installation in progress")
	}
	if *relaunched == "" {
		t.Error("the window did not come back")
	}
}

// A cancelled install says nothing: the person cancelled it themselves.
// The window still comes back.
func TestACancelledInstallIsSilent(t *testing.T) {
	_, pending := helperFixture(t, true)
	stubHelperSeams(t, 1602)
	relaunched := stubRelaunch(t)
	alerted := stubAlert(t)

	if err := RunMSIHelper(pending); err != nil {
		t.Fatalf("helper: %v", err)
	}
	if *alerted {
		t.Error("a message box appeared for an install the person cancelled")
	}
	if *relaunched == "" {
		t.Error("the window did not come back after a cancelled install")
	}
}

// An installed outcome says nothing either: the window coming back is
// the whole of the report.
func TestAnInstalledOutcomeIsSilent(t *testing.T) {
	for _, code := range []int{0, 3010, 1641} {
		_, pending := helperFixture(t, true)
		stubHelperSeams(t, code)
		relaunched := stubRelaunch(t)
		alerted := stubAlert(t)

		if err := RunMSIHelper(pending); err != nil {
			t.Fatalf("helper (exit %d): %v", code, err)
		}
		if *alerted {
			t.Errorf("a message box appeared for exit %d, which installed", code)
		}
		if *relaunched == "" {
			t.Errorf("the window did not come back after exit %d", code)
		}
	}
}

// A terminal parent gets nothing started back, but the parent has
// exited, so a failed install still says so in a message box: nobody
// is at the terminal any more to read a sentence on it.
func TestATerminalFailureAlerts(t *testing.T) {
	_, pending := helperFixture(t, false)
	stubHelperSeams(t, 1603)
	relaunched := stubRelaunch(t)
	var title, text string
	defer func(f func(string, string)) { alertMSI = f }(alertMSI)
	alertMSI = func(gotTitle, gotText string) { title, text = gotTitle, gotText }

	if err := RunMSIHelper(pending); err != nil {
		t.Fatalf("helper: %v", err)
	}
	if title == "" || !strings.Contains(text, "1603") {
		t.Errorf("alert = %q %q, want the exit code for a terminal parent too", title, text)
	}
	if *relaunched != "" {
		t.Errorf("a terminal parent was relaunched as %q", *relaunched)
	}
}

// The message box is an error icon brought to the front. Left behind
// other windows it is silence with one more click attached.
func TestTheAlertIsAnErrorBoxInFront(t *testing.T) {
	const want = 0x10 | 0x10000 | 0x40000 // MB_ICONERROR | MB_SETFOREGROUND | MB_TOPMOST
	if msiAlertFlags != want {
		t.Errorf("msiAlertFlags = %#x, want %#x", msiAlertFlags, want)
	}
}

// Nothing at the window's path means the install failed after the old
// product was removed. With no window left, a message box names the
// exit code and the log.
func TestAMissingWindowAlerts(t *testing.T) {
	dir, pending := helperFixture(t, true)
	if err := os.Remove(filepath.Join(dir, "localcode-gui.exe")); err != nil {
		t.Fatalf("remove the window binary: %v", err)
	}
	stubHelperSeams(t, 1603)
	stubRelaunch(t)
	var title, text string
	defer func(f func(string, string)) { alertMSI = f }(alertMSI)
	alertMSI = func(gotTitle, gotText string) { title, text = gotTitle, gotText }

	if err := RunMSIHelper(pending); err != nil {
		t.Fatalf("helper: %v", err)
	}
	if title == "" || !strings.Contains(text, "1603") {
		t.Errorf("alert = %q %q, want the exit code", title, text)
	}
	rec, _ := ReadMSIRecord(dir)
	if !strings.Contains(text, rec.Log) {
		t.Errorf("alert = %q, want the log path %q", text, rec.Log)
	}
}

// A terminal or a headless daemon gets nothing started back: a new
// console is not the person's terminal.
func TestATerminalParentStartsNothing(t *testing.T) {
	_, pending := helperFixture(t, false)
	stubHelperSeams(t, 0)
	relaunched := stubRelaunch(t)
	alerted := stubAlert(t)

	if err := RunMSIHelper(pending); err != nil {
		t.Fatalf("helper: %v", err)
	}
	if *relaunched != "" {
		t.Errorf("a terminal parent was relaunched as %q", *relaunched)
	}
	if *alerted {
		t.Error("a message box appeared for a terminal parent")
	}
}

// A second request while a helper is waiting or installing is refused,
// and starts no second helper. Busy means the copy refuses removal
// while its pending file is there: the pending file is what says a
// helper is running, not the refusal alone.
func TestASecondInstallWhileBusyIsRefused(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.exe")
	if err := os.WriteFile(src, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(HelperCopyPath(dir, "windows"), []byte("busy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteMSIPending(dir, MSIPending{Version: "0.46.0"}); err != nil {
		t.Fatal(err)
	}
	defer func(f func(string) error) { removeMSIHelper = f }(removeMSIHelper)
	removeMSIHelper = func(helper string) error { return errors.New("file in use") }
	spawned := false
	defer func(f func(string, string, windows.Handle) error) { spawnMSIHelper = f }(spawnMSIHelper)
	spawnMSIHelper = func(helper, pending string, parent windows.Handle) error {
		spawned = true
		return nil
	}

	err := stageMSIInstaller(dir, src, filepath.Join(dir, "x.msi"), "0.47.0", filepath.Join(dir, "localcode.exe"), nil, false)
	var pending *ErrMSIPending
	if !errors.As(err, &pending) {
		t.Fatalf("second install = %v, want a pending refusal", err)
	}
	if pending.Version != "0.46.0" {
		t.Errorf("refusal names %q, want the pending version", pending.Version)
	}
	if spawned {
		t.Error("a second helper was started")
	}
}

// A copy that refuses removal with no pending file is not a busy
// helper: an antivirus scan of the freshly written file, or a read-only
// attribute, refuses too. The request fails saying what failed, rather
// than telling the person an install is pending when none is.
func TestARemovalFailureWithoutPendingSaysSo(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.exe")
	if err := os.WriteFile(src, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(HelperCopyPath(dir, "windows"), []byte("stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	defer func(f func(string) error) { removeMSIHelper = f }(removeMSIHelper)
	removeMSIHelper = func(helper string) error { return errors.New("access denied") }
	spawned := false
	defer func(f func(string, string, windows.Handle) error) { spawnMSIHelper = f }(spawnMSIHelper)
	spawnMSIHelper = func(helper, pending string, parent windows.Handle) error {
		spawned = true
		return nil
	}

	err := stageMSIInstaller(dir, src, filepath.Join(dir, "x.msi"), "0.47.0", filepath.Join(dir, "localcode.exe"), nil, false)
	var pending *ErrMSIPending
	if errors.As(err, &pending) {
		t.Errorf("second install = %v, want the removal failure rather than a pending refusal", err)
	}
	if err == nil || !strings.Contains(err.Error(), "remove the stale install helper") {
		t.Errorf("second install = %v, want it to say what failed", err)
	}
	if spawned {
		t.Error("a helper was started over a copy that refused removal")
	}
}

// The helper starts broken away from its parent's job: a parent inside
// a job that kills its processes when the job closes would otherwise
// take the helper with it.
func TestTheHelperStartsBrokenAwayFromTheParentJob(t *testing.T) {
	defer func(f func(string, string, windows.Handle, uint32) error) { startMSIHelperOnce = f }(startMSIHelperOnce)
	var flags []uint32
	startMSIHelperOnce = func(helper, pending string, parent windows.Handle, f uint32) error {
		flags = append(flags, f)
		return nil
	}
	if err := spawnMSIHelper("helper", "pending", windows.Handle(1234)); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if len(flags) != 1 {
		t.Fatalf("started %d times, want once", len(flags))
	}
	if flags[0]&(detachedProcess|createBreakawayFromJob) != detachedProcess|createBreakawayFromJob {
		t.Errorf("flags = %#x, want detached and breakaway", flags[0])
	}
}

// A job that forbids breakaway refuses with access denied, and then the
// helper starts without the flag instead: inside the job, but running.
func TestTheHelperFallsBackInsideAJobThatForbidsBreakaway(t *testing.T) {
	defer func(f func(string, string, windows.Handle, uint32) error) { startMSIHelperOnce = f }(startMSIHelperOnce)
	denied := fmt.Errorf("start the install helper: %w", windows.ERROR_ACCESS_DENIED)
	var flags []uint32
	startMSIHelperOnce = func(helper, pending string, parent windows.Handle, f uint32) error {
		flags = append(flags, f)
		if len(flags) == 1 {
			return denied
		}
		return nil
	}
	if err := spawnMSIHelper("helper", "pending", windows.Handle(1234)); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if len(flags) != 2 {
		t.Fatalf("started %d times, want the breakaway attempt and the fallback", len(flags))
	}
	if flags[0]&createBreakawayFromJob == 0 {
		t.Errorf("first flags = %#x, want the breakaway attempt first", flags[0])
	}
	if flags[1]&createBreakawayFromJob != 0 {
		t.Errorf("fallback flags = %#x, want the breakaway flag dropped", flags[1])
	}
	if flags[1]&detachedProcess == 0 {
		t.Errorf("fallback flags = %#x, want the helper still detached", flags[1])
	}
}

// Any other start failure is returned, not retried without the flag.
func TestOtherStartFailuresAreNotRetried(t *testing.T) {
	defer func(f func(string, string, windows.Handle, uint32) error) { startMSIHelperOnce = f }(startMSIHelperOnce)
	boom := errors.New("boom")
	starts := 0
	startMSIHelperOnce = func(helper, pending string, parent windows.Handle, f uint32) error {
		starts++
		return boom
	}
	if err := spawnMSIHelper("helper", "pending", windows.Handle(1234)); !errors.Is(err, boom) {
		t.Errorf("spawn = %v, want the start failure", err)
	}
	if starts != 1 {
		t.Errorf("started %d times, want no retry for a failure that is not access denied", starts)
	}
}

// A stale copy from a helper that already exited is removed and the
// install proceeds.
func TestAStaleHelperCopyIsReplaced(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.exe")
	if err := os.WriteFile(src, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(HelperCopyPath(dir, "windows"), []byte("stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	spawned := false
	defer func(f func(string, string, windows.Handle) error) { spawnMSIHelper = f }(spawnMSIHelper)
	spawnMSIHelper = func(helper, pending string, parent windows.Handle) error {
		spawned = true
		return nil
	}
	if err := stageMSIInstaller(dir, src, filepath.Join(dir, "x.msi"), "0.47.0", filepath.Join(dir, "localcode.exe"), nil, false); err != nil {
		t.Fatalf("install over a stale copy: %v", err)
	}
	if !spawned {
		t.Error("no helper was started")
	}
}

// helperFixture stages a pending file and returns its directory and path.
// gui decides whether the parent was the desktop window.
func helperFixture(t *testing.T, gui bool) (dir, pending string) {
	t.Helper()
	dir = t.TempDir()
	parent := filepath.Join(dir, "localcode.exe")
	if gui {
		parent = filepath.Join(dir, "localcode-gui.exe")
		if err := os.WriteFile(filepath.Join(dir, "localcode-gui.exe"), []byte("gui"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	msi := filepath.Join(dir, "localcode-0.46.0-windows-amd64.msi")
	p, err := WriteMSIPending(dir, MSIPending{
		Version: "0.46.0", MSI: msi, Log: MSILogPath(msi, "0.46.0"),
		ParentExe: parent, ParentArgs: []string{"--gui"}, GUI: gui,
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir, p
}

// stubHelperSeams fakes the wait and the installer, returning flags that
// say each ran. Restored with t.Cleanup: a defer here would run when
// this helper returns, undoing every stub before the test body runs,
// and the test would call the real seams instead.
func stubHelperSeams(t *testing.T, code int) (waited, installed *bool) {
	t.Helper()
	waited, installed = new(bool), new(bool)
	oldWait := waitMSIParent
	t.Cleanup(func() { waitMSIParent = oldWait })
	waitMSIParent = func(raw string) error { *waited = true; return nil }
	oldRun := runMSIInstaller
	t.Cleanup(func() { runMSIInstaller = oldRun })
	runMSIInstaller = func(msi, log string) int { *installed = true; return code }
	return waited, installed
}

func stubRelaunch(t *testing.T) *string {
	t.Helper()
	target := new(string)
	old := relaunchMSI
	t.Cleanup(func() { relaunchMSI = old })
	relaunchMSI = func(got string, args []string) error { *target = got; return nil }
	return target
}

func stubAlert(t *testing.T) *bool {
	t.Helper()
	alerted := new(bool)
	old := alertMSI
	t.Cleanup(func() { alertMSI = old })
	alertMSI = func(title, text string) { *alerted = true }
	return alerted
}

// The wait runs against a real process handle: it returns only after
// the process exits. A stub that ignores its argument cannot see the
// bug this guards: the helper unset the handle's environment variable
// before reading it, so every install failed in the wait and msiexec
// never ran.
func TestTheWaitBlocksUntilTheParentExits(t *testing.T) {
	// A child that sleeps about two seconds, the stand-in parent.
	cmd := exec.Command("cmd", "/c", "ping -n 3 127.0.0.1 >NUL")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the stand-in parent: %v", err)
	}
	// An inheritable duplicate of a handle to the child, the way the
	// parent passes itself to the helper (see inheritableParentHandle).
	h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_DUP_HANDLE, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatalf("open the stand-in parent: %v", err)
	}
	cur := windows.CurrentProcess()
	var dup windows.Handle
	if err := windows.DuplicateHandle(cur, h, cur, &dup, 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
		windows.CloseHandle(h)
		t.Fatalf("duplicate the stand-in parent's handle: %v", err)
	}
	windows.CloseHandle(h)
	defer windows.CloseHandle(dup)

	done := make(chan error, 1)
	go func() { done <- waitMSIParent(strconv.FormatUint(uint64(dup), 10)) }()

	select {
	case err := <-done:
		t.Fatalf("the wait returned while the parent was still running: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the stand-in parent exited uncleanly: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the wait failed after the parent exited: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the wait did not return after the parent exited")
	}
}

// One value decides both the reply and the pending file: the daemon's
// DesktopWindow, passed in as window. The reply used to be derived from
// the executable's name while the pending file carried the daemon's
// flag, so the two could disagree about whether the window comes back.
func TestApplyForCarriesOneWindowValueToReplyAndPending(t *testing.T) {
	for _, window := range []bool{true, false} {
		name := "terminal"
		if window {
			name = "window"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			msi := filepath.Join(dir, "localcode-0.46.0-windows-amd64.msi")
			if err := os.WriteFile(msi, []byte("msi"), 0o644); err != nil {
				t.Fatal(err)
			}
			old := msiInstallDir
			msiInstallDir = func() (string, error) { return dir, nil }
			defer func() { msiInstallDir = old }()
			defer func(f func(string, string, windows.Handle) error) { spawnMSIHelper = f }(spawnMSIHelper)
			spawnMSIHelper = func(helper, pending string, parent windows.Handle) error { return nil }

			out, err := ApplyFor(msi, window)
			if err != nil {
				t.Fatalf("ApplyFor: %v", err)
			}
			if !out.Started {
				t.Error("Started = false, want the staged installer")
			}
			if want := MSIDetail("0.46.0", window); out.Detail != want {
				t.Errorf("Detail = %q, want %q", out.Detail, want)
			}
			p, err := ReadMSIPending(MSIPendingPath(dir))
			if err != nil {
				t.Fatalf("no pending file: %v", err)
			}
			if p.GUI != window {
				t.Errorf("pending GUI = %v, want %v: the reply promises one thing and the helper does the other", p.GUI, window)
			}
		})
	}
}

// The wait receives the handle value that was put in the environment.
// The helper unsets its mode so msiexec and the relaunched window do
// not inherit it, and reading the value after unsetting it waits on
// nothing and fails every install.
func TestTheHelperPassesTheEnvironmentHandleToTheWait(t *testing.T) {
	_, pending := helperFixture(t, true)
	defer func(f func(string, string) int) { runMSIInstaller = f }(runMSIInstaller)
	runMSIInstaller = func(msi, log string) int { return 0 }
	stubRelaunch(t)
	stubAlert(t)

	const handle = "98765"
	t.Setenv(EnvMSIParent, handle)
	var got string
	defer func(f func(string) error) { waitMSIParent = f }(waitMSIParent)
	waitMSIParent = func(raw string) error { got = raw; return nil }

	if err := RunMSIHelper(pending); err != nil {
		t.Fatalf("helper: %v", err)
	}
	if got != handle {
		t.Errorf("the wait got %q, want the handle %q from the environment", got, handle)
	}
}
