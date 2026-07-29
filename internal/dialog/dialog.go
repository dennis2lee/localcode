// Package dialog opens the operating system's own folder-picker so the
// desktop window can offer "choose a workspace" the way any native app
// does, instead of asking someone to type an absolute path.
//
// It shells out to a per-OS helper (osascript, PowerShell, zenity/kdialog)
// rather than binding a native toolkit. That keeps the whole package pure
// Go with no CGo: the GUI already costs a CGo-linked webview that cannot be
// cross-compiled, and adding a second one here would push that cost onto
// the plain daemon and TUI builds too, which are cross-compiled from one
// machine for every release.
//
// The web platform has no equivalent — neither <input webkitdirectory> nor
// showDirectoryPicker() exposes an absolute filesystem path, which is
// exactly what the caller needs — so this is the only way to get one.
package dialog

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// ErrCancelled is returned when the user dismissed the dialog without
// choosing anything. It is a normal outcome, not a failure: callers should
// leave everything as it was rather than reporting an error.
var ErrCancelled = errors.New("dialog cancelled")

// ErrUnsupported is returned on a platform with no picker available — a
// Linux box with neither zenity nor kdialog installed, mainly. Callers fall
// back to typing a path.
var ErrUnsupported = errors.New("no native folder picker available on this system")

// Available reports whether PickDirectory can actually open a dialog here,
// so a caller can hide the button instead of offering one that always
// fails.
func Available() bool {
	switch runtime.GOOS {
	case "darwin", "windows":
		return true
	default:
		return linuxHelper() != ""
	}
}

// PickDirectory opens a folder picker and returns the absolute path chosen.
// startDir, if set, is where the dialog opens. The call blocks for as long
// as the dialog is up — cancel ctx (e.g. when the requesting client goes
// away) to tear it down.
func PickDirectory(ctx context.Context, title, startDir string) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		return pickDarwin(ctx, title, startDir)
	case "windows":
		return pickWindows(ctx, title, startDir)
	default:
		return pickLinux(ctx, title, startDir)
	}
}

func pickDarwin(ctx context.Context, title, startDir string) (string, error) {
	script := fmt.Sprintf(`choose folder with prompt %s`, appleScriptString(title))
	if startDir != "" {
		script += fmt.Sprintf(` default location POSIX file %s`, appleScriptString(startDir))
	}
	// "activate" first, otherwise the dialog can open behind the window
	// that asked for it — osascript is its own process and does not
	// inherit the caller's frontmost status.
	full := "activate\nset chosen to " + script + "\nPOSIX path of chosen"

	out, err := exec.CommandContext(ctx, "osascript", "-e", full).Output()
	if err != nil {
		// osascript exits 1 both for a cancelled dialog and for a script
		// error, so the message is the only thing that tells them apart.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && strings.Contains(string(exitErr.Stderr), "User canceled") {
			return "", ErrCancelled
		}
		return "", fmt.Errorf("osascript: %w%s", err, stderrSuffix(err))
	}
	return cleanPath(string(out))
}

// windowsPickerScript uses WinForms' FolderBrowserDialog, which is
// available on any Windows with .NET Framework (i.e. all of them) and
// needs no extra install. -STA is required: WinForms dialogs cannot run on
// a multi-threaded apartment, and PowerShell defaults to MTA for -Command.
const windowsPickerScript = `
Add-Type -AssemblyName System.Windows.Forms
$d = New-Object System.Windows.Forms.FolderBrowserDialog
$d.Description = $env:LOCALCODE_DIALOG_TITLE
$d.ShowNewFolderButton = $true
if ($env:LOCALCODE_DIALOG_START) { $d.SelectedPath = $env:LOCALCODE_DIALOG_START }
if ($d.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { [Console]::Out.Write($d.SelectedPath) }
`

func pickWindows(ctx context.Context, title, startDir string) (string, error) {
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-STA", "-NonInteractive", "-Command", windowsPickerScript)
	// Passed through the environment rather than interpolated into the
	// script, so a path containing a quote or a $ cannot become code.
	cmd.Env = append(cmd.Environ(),
		"LOCALCODE_DIALOG_TITLE="+title,
		"LOCALCODE_DIALOG_START="+startDir,
	)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("powershell folder picker: %w%s", err, stderrSuffix(err))
	}
	if strings.TrimSpace(string(out)) == "" {
		return "", ErrCancelled // the script writes nothing unless OK was clicked
	}
	return cleanPath(string(out))
}

// linuxHelper returns whichever supported dialog program is installed, or
// "" if none is.
func linuxHelper() string {
	for _, name := range []string{"zenity", "kdialog"} {
		if _, err := exec.LookPath(name); err == nil {
			return name
		}
	}
	return ""
}

func pickLinux(ctx context.Context, title, startDir string) (string, error) {
	var cmd *exec.Cmd
	switch linuxHelper() {
	case "zenity":
		args := []string{"--file-selection", "--directory", "--title", title}
		if startDir != "" {
			args = append(args, "--filename", strings.TrimSuffix(startDir, "/")+"/")
		}
		cmd = exec.CommandContext(ctx, "zenity", args...)
	case "kdialog":
		start := startDir
		if start == "" {
			start = "."
		}
		cmd = exec.CommandContext(ctx, "kdialog", "--title", title, "--getexistingdirectory", start)
	default:
		return "", ErrUnsupported
	}

	out, err := cmd.Output()
	if err != nil {
		// Both helpers exit non-zero purely because the user hit Cancel,
		// which is why this isn't reported as an error.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", ErrCancelled
		}
		return "", fmt.Errorf("folder picker: %w", err)
	}
	return cleanPath(string(out))
}

func cleanPath(out string) (string, error) {
	path := strings.TrimSpace(out)
	// AppleScript's POSIX path yields a trailing slash for directories;
	// every other caller here treats paths without one.
	if len(path) > 1 {
		path = strings.TrimSuffix(path, "/")
	}
	if path == "" {
		return "", ErrCancelled
	}
	return path, nil
}

// appleScriptString quotes a Go string as an AppleScript string literal.
// Only backslash and double quote are special inside one.
func appleScriptString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// stderrSuffix appends a failing helper's stderr to an error message, since
// the exit status alone ("exit status 1") says nothing useful.
func stderrSuffix(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		return ": " + strings.TrimSpace(string(exitErr.Stderr))
	}
	return ""
}
