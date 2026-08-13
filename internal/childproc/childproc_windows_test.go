//go:build windows

package childproc

import (
	"os/exec"
	"testing"
)

// The bug this guards: RevealDirectory used Hide on explorer.exe, and
// Hide's STARTF_USESHOWWINDOW + SW_HIDE is applied by Windows to the first
// top-level window the child creates. explorer.exe is a GUI program whose
// whole job is to create one, so the folder window opened hidden — a button
// that produced no window, no error, and nothing in the transcript.
func TestHideConsoleDoesNotHideTheChildsOwnWindow(t *testing.T) {
	cmd := exec.Command("explorer.exe", `C:\`)
	HideConsole(cmd)

	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Error("HideConsole should still suppress a console window")
	}
	if cmd.SysProcAttr.HideWindow {
		t.Error("HideConsole set SW_HIDE — the window we are starting the child to open would not appear")
	}
}

// And the other half: Hide, for console children, must keep doing both.
func TestHideStillSuppressesBoth(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "echo hi")
	Hide(cmd)

	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Error("Hide dropped CREATE_NO_WINDOW")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Error("Hide dropped SW_HIDE")
	}
}
