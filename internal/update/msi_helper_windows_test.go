//go:build windows

package update

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
// path is what the person has.
func TestAFailedInstallStillRelaunches(t *testing.T) {
	dir, pending := helperFixture(t, true)
	waited, installed := stubHelperSeams(t, 1603)
	relaunched := stubRelaunch(t)
	stubAlert(t)

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
	if *relaunched == "" {
		t.Error("a failed install did not bring the window back")
	}
}

// Nothing at the window's path means the install failed after the old
// product was removed. With no window left, a message box names the
// exit code and the log.
func TestAMissingWindowAlerts(t *testing.T) {
	dir, pending := helperFixture(t, true)
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
// and starts no second helper.
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
	defer func(f func(string) bool) { msiHelperInUse = f }(msiHelperInUse)
	msiHelperInUse = func(helper string) bool { return true }
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
// say each ran.
func stubHelperSeams(t *testing.T, code int) (waited, installed *bool) {
	t.Helper()
	waited, installed = new(bool), new(bool)
	defer func(f func(string) error) { waitMSIParent = f }(waitMSIParent)
	waitMSIParent = func(raw string) error { *waited = true; return nil }
	defer func(f func(string, string) int) { runMSIInstaller = f }(runMSIInstaller)
	runMSIInstaller = func(msi, log string) int { *installed = true; return code }
	return waited, installed
}

func stubRelaunch(t *testing.T) *string {
	t.Helper()
	target := new(string)
	defer func(f func(string, []string) error) { relaunchMSI = f }(relaunchMSI)
	relaunchMSI = func(got string, args []string) error { *target = got; return nil }
	return target
}

func stubAlert(t *testing.T) *bool {
	t.Helper()
	alerted := new(bool)
	defer func(f func(string, string)) { alertMSI = f }(alertMSI)
	alertMSI = func(title, text string) { *alerted = true }
	return alerted
}
