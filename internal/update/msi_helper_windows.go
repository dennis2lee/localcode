//go:build windows

package update

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"localcode/internal/childproc"
)

// The Windows half of the MSI install: staging a helper that outlives
// its parent, and the helper itself.
//
// The installer must not run while the localcode process that asked for
// it still holds files under the install directory. The Restart Manager
// cannot close that process: it classifies the window as RmUnknownApp,
// which it closes only by force, and Windows Installer does not force
// it. So the MSI waits on a files-in-use dialog instead of installing.
//
// The helper fixes the ordering. It is a copy of the running executable
// under the updates directory, holding no file under the install
// directory. It waits for the parent to exit, runs msiexec, records the
// result, and starts the window again. The parent only stages and starts
// it, then answers the install button and carries on until the person
// closes it. There is no timeout on the wait: the person decides when
// localcode closes.

// detachedProcess lets the helper outlive its parent with no console of
// its own.
const detachedProcess = 0x00000008

// msiUpdatesDir is where the MSI, the helper copy, the pending file, the
// record and the log live. The user's cache directory, the same place
// downloads already go (see internal/daemon/update.go).
func msiUpdatesDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("no cache directory to stage the install in: %w", err)
	}
	return filepath.Join(base, "localcode", "updates"), nil
}

// stageMSIInstaller copies the running executable into the updates
// directory, writes what the helper should do, and starts it hidden and
// detached. dir is the updates directory. srcExe is the file
// copied (the running executable in production). parentExe and parentArgs
// say where the parent ran from and with what, for the relaunch.
func stageMSIInstaller(dir, srcExe, msi, version, parentExe string, parentArgs []string, gui bool) error {
	log := MSILogPath(msi, version)
	helper := HelperCopyPath(dir, "windows")

	// A helper cannot delete itself while it runs, so a copy that is
	// still there is either stale or busy. A stale copy removes cleanly
	// and its pending file with it. A copy that refuses removal is a
	// helper still waiting or installing, and a second request is
	// refused rather than starting a second helper.
	if _, err := os.Stat(helper); err == nil {
		if msiHelperInUse(helper) {
			pendingVersion := version
			if p, perr := ReadMSIPending(MSIPendingPath(dir)); perr == nil && p.Version != "" {
				pendingVersion = p.Version
			}
			return &ErrMSIPending{Version: pendingVersion}
		}
		_ = ClearMSIPending(dir)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// A new install supersedes the last record: the check reports the
	// install that was asked for, not the one before it.
	_ = ClearMSIRecord(dir)
	if err := copyFile(srcExe, helper); err != nil {
		return fmt.Errorf("stage the install helper: %w", err)
	}
	pending, err := WriteMSIPending(dir, MSIPending{
		Version:    version,
		MSI:        msi,
		Log:        log,
		ParentExe:  parentExe,
		ParentArgs: append([]string(nil), parentArgs...),
		GUI:        gui,
		Time:       time.Now(),
	})
	if err != nil {
		return err
	}

	parent, err := inheritableParentHandle()
	if err != nil {
		return err
	}
	return spawnMSIHelper(helper, pending, parent)
}

// msiHelperInUse reports whether a helper copy is running: one that
// refuses removal. A variable so a test can fake a busy helper without
// running one.
var msiHelperInUse = func(helper string) bool {
	return os.Remove(helper) != nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// inheritableParentHandle is a real, inheritable handle to this process.
// The helper waits on it, which no PID reuse can fool: the handle refers
// to the process object itself.
func inheritableParentHandle() (windows.Handle, error) {
	cur := windows.CurrentProcess()
	var dup windows.Handle
	if err := windows.DuplicateHandle(cur, windows.Handle(cur), cur, &dup, 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return 0, fmt.Errorf("duplicate the parent process handle: %w", err)
	}
	return dup, nil
}

// spawnMSIHelper starts the helper copy hidden and detached, with the
// pending path and the parent handle in its environment. Hidden through
// HideConsole rather than Hide: Hide's SHOWWINDOW + SW_HIDE is inherited
// by the first top-level window a GUI child creates, and the helper's
// failure message box is a window meant to be seen.
var spawnMSIHelper = func(helper, pending string, parent windows.Handle) error {
	nul, err := os.OpenFile("NUL", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer nul.Close()
	cmd := exec.Command(helper)
	cmd.Env = append(os.Environ(),
		EnvMSIHelper+"="+pending,
		EnvMSIParent+"="+strconv.FormatUint(uint64(parent), 10),
	)
	cmd.Stdout = nul
	cmd.Stderr = nul
	childproc.HideConsole(cmd)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= detachedProcess
	cmd.SysProcAttr.AdditionalInheritedHandles = []syscall.Handle{syscall.Handle(parent)}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the install helper: %w", err)
	}
	// Released rather than waited for: this process is about to keep
	// serving until the person closes it, and the helper outlives it on
	// purpose.
	return cmd.Process.Release()
}

// SweepMSIHelper removes a stale helper copy at startup. A copy in use
// refuses removal and is left alone: a helper is running.
func SweepMSIHelper() {
	dir, err := msiUpdatesDir()
	if err != nil {
		return
	}
	_ = os.Remove(HelperCopyPath(dir, "windows"))
}

// RunMSIHelper is the helper's main: wait for the parent to exit, run
// the installer, record the result, and start the window again. It runs
// before anything else in main, before the desktop window opens
// anything.
func RunMSIHelper(pendingPath string) error {
	// Read before unsetting: the wait below needs the parent's handle,
	// and after this nothing the helper starts may see either variable.
	// Unset before anything is started: msiexec and the relaunched
	// window inherit this process's environment, and either one seeing
	// the helper's mode would mistake itself for the helper.
	parentRaw := os.Getenv(EnvMSIParent)
	os.Unsetenv(EnvMSIHelper)
	os.Unsetenv(EnvMSIParent)
	p, err := ReadMSIPending(pendingPath)
	if err != nil {
		return err
	}
	if err := waitMSIParent(parentRaw); err != nil {
		return err
	}
	code := runMSIInstaller(p.MSI, p.Log)
	dir := filepath.Dir(pendingPath)
	_ = WriteMSIRecord(dir, MSIRecord{
		Version:  p.Version,
		ExitCode: code,
		Log:      p.Log,
		Time:     time.Now(),
	})
	_ = ClearMSIPending(dir)

	// Whatever is at the window's path is what the person has, so the
	// window comes back whether the install succeeded, was cancelled,
	// or failed. A terminal gets nothing back: a new console is not the
	// person's terminal. But the parent has exited by now, terminal or
	// not, so nobody is watching for a sentence on it either: an
	// outcome that is neither installed nor cancelled (failed, another
	// installation in progress) says so in a message box first, for a
	// terminal parent too. Cancelled says nothing, because the person
	// cancelled it themselves. The box comes before the relaunch, so
	// the person reads what happened before the window is back.
	status, installed := ClassifyMSIExit(code)
	failed := !installed && status != "cancelled"
	if !p.GUI {
		if failed {
			alertMSI("LocalCode update failed", msiFailureText(p, code))
		}
		return nil
	}
	target := GUIExecutableBeside(p.ParentExe)
	if _, err := os.Stat(target); err != nil {
		// No window left to say it, so a message box says it: the exit
		// code and the log. This is the end state the bug report
		// described, an old product removed and no new one installed,
		// and silence about it would be the same failure again. One
		// box: a failed install with no window says it here rather
		// than twice.
		title := "LocalCode update failed"
		if installed {
			title = "LocalCode update"
		}
		alertMSI(title, msiFailureText(p, code))
		return nil
	}
	if failed {
		alertMSI("LocalCode update failed", msiFailureText(p, code))
	}
	return relaunchMSI(target, p.ParentArgs)
}

// msiFailureText is what the message box names: the version, the exit
// code and what it means, and the log. The panel never shows it — the
// check reports the same record — so this box is the only place it is
// said when the window is about to come back over it.
func msiFailureText(p MSIPending, code int) string {
	return fmt.Sprintf("LocalCode %s: the installer exited %d (%s). The installer log is at %s.",
		p.Version, code, MSIExitMeaning(code), p.Log)
}

// waitMSIParent waits for the parent process to exit, by the inherited
// handle rather than by PID. No timeout: the person decides when
// localcode closes.
var waitMSIParent = func(raw string) error {
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || v == 0 {
		return fmt.Errorf("the install helper has no parent to wait for")
	}
	h := windows.Handle(v)
	_, err = windows.WaitForSingleObject(h, windows.INFINITE)
	if err != nil {
		return fmt.Errorf("wait for localcode to exit: %w", err)
	}
	return nil
}

// runMSIInstaller runs msiexec and waits for it, because the record and
// the relaunch both need the exit code. The log sits beside the MSI and
// is named after the version.
//
// /qb, and it is the difference between an update that applies and one
// that does not. The package has no authored files-in-use dialog (wixl
// cannot author dialogs), so at full UI a missing dialog falls back to
// scheduling a reboot instead of replacing the files. Basic UI has a
// built-in dialog. Nobody is holding the files any more by the time this
// runs — the helper waited for that — so the dialog has nothing to wait
// on either.
//
// No /quiet: replacing the program is not something to do behind a
// progress bar nobody can see or cancel. /l*v keeps a full log, since a
// failed upgrade with no log is what started this.
var runMSIInstaller = func(msi, log string) int {
	cmd := exec.Command("msiexec", installerArgs(msi, log)...)
	if err := cmd.Start(); err != nil {
		return 1
	}
	if err := cmd.Wait(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode()
		}
		return 1
	}
	return 0
}

// relaunchMSI starts the window again from the path the parent ran from,
// with the parent's arguments.
var relaunchMSI = func(target string, args []string) error {
	cmd := exec.Command(target, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s again: %w", target, err)
	}
	return cmd.Process.Release()
}

// msiAlertFlags is how the message box is shown: an error icon, and
// brought to the front. The box reports an install that has already
// happened, behind whatever the person has open since; left behind
// other windows it is silence with one more click attached.
const msiAlertFlags = 0x10 | 0x10000 | 0x40000 // MB_ICONERROR | MB_SETFOREGROUND | MB_TOPMOST

// alertMSI shows a message box, for when there is no window left to say
// what happened.
var alertMSI = func(title, text string) {
	dll := syscall.NewLazyDLL("user32.dll")
	proc := dll.NewProc("MessageBoxW")
	t, _ := syscall.UTF16PtrFromString(text)
	c, _ := syscall.UTF16PtrFromString(title)
	_, _, _ = proc.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), msiAlertFlags)
}
