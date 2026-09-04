//go:build gui && windows

package gui

import (
	"os"
	"strings"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Coming back after the installer.
//
// The settings window's install button runs the MSI, and the MSI has to
// close localcode to replace its files: Windows Installer asks the
// Restart Manager to shut down whatever holds them, and this window is
// what holds them. What used to happen next was nothing. The install
// finished, the window was gone, and the person who had clicked "install"
// went to the Start menu to open the program they were just using.
//
// The Restart Manager can put an application back after the install that
// closed it, provided the application asked first: RegisterApplicationRestart,
// called once, early, with the command line to restart with. This is the
// mechanism every self-updating desktop program on the platform uses, and
// it is a single call. It was not used for the terminal because the
// Restart Manager restarts a console program in a NEW console, which is
// not the terminal the person is sitting in front of (see
// cmd/localcode/restart_windows.go). A window has no such problem: a new
// window is exactly what is wanted.
//
// Two documented limits. The application must have been running for
// sixty seconds before the shutdown, so a window updated within a minute
// of opening is not brought back; and the restart is the Restart
// Manager's to perform, so an installer that bypasses it (or a reboot
// that is needed first) does not trigger one. Both are Windows' rules,
// and the reply the install button gets says "when the install finishes"
// rather than promising more.

const (
	restartNoCrash  = 1
	restartNoHang   = 2
	restartNoReboot = 8
)

var restartRegistered atomic.Bool

// registerRestart asks Windows to start this program again, with the
// same arguments, after an installer shuts it down. Best effort: a
// refusal leaves things as they were, which is the window not coming
// back, and InstallerRestarts says so.
func registerRestart() {
	k := windows.NewLazySystemDLL("kernel32.dll")
	proc := k.NewProc("RegisterApplicationRestart")
	if err := proc.Find(); err != nil {
		return
	}
	// The arguments only: Windows supplies the executable, which is the
	// one that just got replaced under the same path.
	args, err := windows.UTF16PtrFromString(strings.Join(os.Args[1:], " "))
	if err != nil {
		return
	}
	// Restart after an update only, not after a crash, a hang, or on
	// reboot. A window that crashed and then reopened itself would hide
	// the crash; the install is the one case where coming back is the
	// point.
	flags := uintptr(restartNoCrash | restartNoHang | restartNoReboot)
	if r, _, _ := proc.Call(uintptr(unsafe.Pointer(args)), flags); r == 0 {
		restartRegistered.Store(true)
	}
}

// InstallerRestarts reports whether Windows has agreed to bring this
// window back after an installer closes it.
func InstallerRestarts() bool { return restartRegistered.Load() }
