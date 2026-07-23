package shell

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func noLookPath(string) (string, error) { return "", errors.New("not found") }
func noEnv(string) string               { return "" }
func noFile(string) bool                { return false }

func winEnv(name string) string {
	switch name {
	case "ProgramFiles":
		return `C:\Program Files`
	case "ProgramFiles(x86)":
		return `C:\Program Files (x86)`
	case "LocalAppData":
		return `C:\Users\u\AppData\Local`
	case "ComSpec":
		return `C:\Windows\System32\cmd.exe`
	}
	return ""
}

func TestResolveUnixIsShUnchanged(t *testing.T) {
	got := resolve("linux", noLookPath, noEnv, noFile)
	if got.path != "sh" || len(got.args) != 1 || got.args[0] != "-c" || !got.posix {
		t.Errorf("resolve(linux) = %+v, want plain sh -c — non-Windows behavior must not change", got)
	}
}

// TestResolveWindowsPrefersShOnPath: Git for Windows on PATH provides
// sh.exe, the best case — bash-flavored commands run unmodified.
func TestResolveWindowsPrefersShOnPath(t *testing.T) {
	look := func(name string) (string, error) {
		if name == "sh" {
			return `C:\Program Files\Git\usr\bin\sh.exe`, nil
		}
		return "", errors.New("not found")
	}
	got := resolve("windows", look, winEnv, noFile)
	if !strings.HasSuffix(got.path, "sh.exe") || !got.posix {
		t.Errorf("resolve(windows, sh on PATH) = %+v, want the PATH sh.exe", got)
	}
}

// TestResolveWindowsFindsGitBashOffPath covers a Git install that never
// put its shell tools on PATH — the exact machine in the bug report.
func TestResolveWindowsFindsGitBashOffPath(t *testing.T) {
	bash := filepath.Join(`C:\Program Files`, "Git", "bin", "bash.exe")
	exists := func(p string) bool { return p == bash }
	got := resolve("windows", noLookPath, winEnv, exists)
	if got.path != bash || !got.posix {
		t.Errorf("resolve(windows, git bash off PATH) = %+v, want %s", got, bash)
	}
}

// TestResolveWindowsFallsBackToCmd: with no POSIX shell anywhere the tool
// must still work rather than fail every call — cmd handles the common
// cases (git, go, simple pipes) and Notice() warns the model about the
// rest.
func TestResolveWindowsFallsBackToCmd(t *testing.T) {
	got := resolve("windows", noLookPath, winEnv, noFile)
	if got.path != `C:\Windows\System32\cmd.exe` || len(got.args) != 1 || got.args[0] != "/c" {
		t.Errorf("resolve(windows, nothing found) = %+v, want ComSpec /c", got)
	}
	if got.posix {
		t.Error("cmd fallback must report posix=false so Notice() fires")
	}
}

func TestResolveWindowsCmdWithoutComSpec(t *testing.T) {
	got := resolve("windows", noLookPath, noEnv, noFile)
	if got.path != "cmd" {
		t.Errorf("resolve(windows, empty env) = %+v, want bare \"cmd\"", got)
	}
}

// TestCommandRunsOnHost is the one live check: whatever the host resolves
// to must actually execute a trivial command.
func TestCommandRunsOnHost(t *testing.T) {
	out, err := Command(context.Background(), "echo shell-ok").CombinedOutput()
	if err != nil {
		t.Fatalf("Command failed on the host shell: %v (%s)", err, out)
	}
	if !strings.Contains(string(out), "shell-ok") {
		t.Errorf("output = %q, want it to contain shell-ok", out)
	}
}

func TestNoticeQuietOnPosixHosts(t *testing.T) {
	// On every dev/CI host we run tests on, a POSIX sh exists, so the
	// notice must be empty — it is reserved for the cmd fallback.
	if current().posix && Notice() != "" {
		t.Errorf("Notice() = %q on a POSIX host, want empty", Notice())
	}
}
