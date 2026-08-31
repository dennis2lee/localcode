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

// The Store alias stub, caught before launch. The message replaces four
// blind python retries and a popup nobody can act on with one error the
// model can act on.
func TestStoreStubIsDetectedBeforeLaunch(t *testing.T) {
	stub := func(name string) (string, error) {
		return `C:\Users\u\AppData\Local\Microsoft\WindowsApps\` + name + `.exe`, nil
	}
	real := func(name string) (string, error) {
		return `C:\Python312\` + name + `.exe`, nil
	}

	if msg, is := storeStub("windows", `python3 -c "print(1)"`, stub); !is {
		t.Error("the python3 alias stub was not detected")
	} else if !strings.Contains(msg, "winget install") || !strings.Contains(msg, pythonWingetID) {
		t.Errorf("the message does not name the install command: %q", msg)
	}
	// A real installation is not a stub.
	if _, is := storeStub("windows", `python3 -c "print(1)"`, real); is {
		t.Error("a real python install was called a stub")
	}
	// python as an argument is not python as a command.
	if _, is := storeStub("windows", `grep python3 notes.txt`, stub); is {
		t.Error("python3 as an argument tripped the detector")
	}
	// But the second command of a pipeline or chain is a command.
	if _, is := storeStub("windows", `echo hi && python3 x.py`, stub); !is {
		t.Error("python3 after && was not detected")
	}
	// Off Windows the aliases do not exist.
	if _, is := storeStub("darwin", `python3 -c "print(1)"`, stub); is {
		t.Error("the detector fired off Windows")
	}
	// A command that is not python is not looked up at all.
	if _, is := storeStub("windows", `node -e "1"`, func(string) (string, error) {
		t.Error("lookPath was called for a non-python command")
		return "", nil
	}); is {
		t.Error("node was called a stub")
	}
}

// The message is an instruction, and which instruction depends on
// whether winget is actually there. Recommending a command that does
// not exist is how the original failure got its second life.
func TestStubMessageDependsOnWingetBeingAvailable(t *testing.T) {
	withWinget := func(name string) (string, error) {
		if name == "winget" {
			return `C:\Windows\System32\winget.exe`, nil
		}
		return `C:\Users\u\AppData\Local\Microsoft\WindowsApps\` + name + `.exe`, nil
	}
	withoutWinget := func(name string) (string, error) {
		if name == "winget" {
			return "", errors.New("not found")
		}
		return `C:\Users\u\AppData\Local\Microsoft\WindowsApps\` + name + `.exe`, nil
	}

	msg, is := storeStub("windows", "python3 x.py", withWinget)
	if !is {
		t.Fatal("the stub was not detected")
	}
	// "py -3" used to be here and is deliberately gone: the launcher
	// resolves PEP 514 PythonCore registrations, which a conda, miniforge
	// or uv-managed interpreter is not, so on the machines this feature
	// is most often needed the advice pointed at nothing.
	for _, want := range []string{"winget install", pythonWingetID, "winget search", "absolute path"} {
		if !strings.Contains(msg, want) {
			t.Errorf("with winget present, the message is missing %q:\n%s", want, msg)
		}
	}
	// The PATH caveat matters: the install cannot reach a shell that is
	// already running, so chaining it onto the same command fails and
	// looks like the install failed.
	if !strings.Contains(msg, "already running") {
		t.Errorf("the message does not warn that the install misses the current shell:\n%s", msg)
	}

	msg, is = storeStub("windows", "python3 x.py", withoutWinget)
	if !is {
		t.Fatal("the stub was not detected")
	}
	if strings.Contains(msg, "winget install") {
		t.Errorf("a machine without winget was told to run winget:\n%s", msg)
	}
	if !strings.Contains(msg, "App execution aliases") || !strings.Contains(msg, "node, awk") {
		t.Errorf("without winget, the message does not give a workable path:\n%s", msg)
	}
}
