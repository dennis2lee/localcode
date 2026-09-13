package hooks

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// hookWriteCommand produces a shell command string that writes a known string
// to a given path, portable across Unix shells (sh) and Windows environments
// (whether running under Git Bash or cmd.exe).
//
// On Unix, a standard redirection `echo text > path` works. On Windows, tests
// failed because Windows temp paths (e.g. `C:\Users\runneradmin\...\001\ran`) contain
// backslashes that Git Bash parses as escape sequences (`\001` becomes an octal escape,
// `\r` becomes a carriage return), redirecting output to a corrupted filename and
// causing os.Stat to fail. When falling back to cmd.exe, POSIX-only commands like cat
// and pwd fail completely. Converting paths to slash notation ensures paths are safe
// under both bash and cmd.exe without escaping corruption.
func toSlash(path string) string {
	return strings.ReplaceAll(path, `\`, "/")
}

func hookWriteCommand(path, text string) string {
	return hookWriteCommandFor(runtime.GOOS, path, text)
}

func hookWriteCommandFor(goos, path, text string) string {
	if goos == "windows" {
		return fmt.Sprintf("echo %s> %s", text, toSlash(path))
	}
	return fmt.Sprintf("echo %s > %s", text, path)
}

// hookStdinCommand produces a shell command string that captures stdin to a file
// at path, portable across Unix sh and Windows environments.
//
// In a pure POSIX environment, `cat > path` reads stdin to EOF and writes it.
// On Windows, Git for Windows provides `cat`, but systems falling back to cmd.exe
// lack it. Using `(cat 2>nul || more) > path` allows Git Bash to execute `cat` cleanly
// while falling back to Windows' built-in `more` under cmd.exe without leaving an
// unhandled failure.
func hookStdinCommand(path string) string {
	return hookStdinCommandFor(runtime.GOOS, path)
}

func hookStdinCommandFor(goos, path string) string {
	if goos == "windows" {
		return fmt.Sprintf("(cat 2>nul || more) > %s", toSlash(path))
	}
	return fmt.Sprintf("cat > %s", path)
}

// hookPwdCommand produces a shell command string that records the current working
// directory to path, portable across Unix sh and Windows environments.
//
// In POSIX sh, `pwd` prints the working directory. Under cmd.exe, `pwd` is unrecognized
// and `cd` with no arguments prints the current directory. Under Git Bash, bare `cd`
// changes directory to $HOME instead of printing it. The fallback `(pwd 2>nul || cd)`
// lets Git Bash execute `pwd` and cmd.exe execute `cd`.
func hookPwdCommand(path string) string {
	return hookPwdCommandFor(runtime.GOOS, path)
}

func hookPwdCommandFor(goos, path string) string {
	if goos == "windows" {
		return fmt.Sprintf("(pwd 2>nul || cd) > %s", toSlash(path))
	}
	return fmt.Sprintf("pwd > %s", path)
}

// TestHookCommandsArePortableAcrossOperatingSystems guards the requirement that
// hook commands generated for test fixtures must remain portable across operating
// systems rather than assuming a Unix shell.
//
// The requirement is that Windows hook commands must never emit unescaped backslashes
// into shell scripts (which bash treats as escape sequences, turning \001 into a byte
// rather than a directory name), must not assume POSIX utilities exist under cmd.exe,
// and must target the intended file. On Unix, commands must adhere to POSIX sh syntax
// without leaking Windows fallback idioms.
func TestHookCommandsArePortableAcrossOperatingSystems(t *testing.T) {
	const (
		winPath  = `C:\Users\runneradmin\AppData\Local\Temp\TestRun001\ran`
		unixPath = `/tmp/test-run-001/ran`
		text     = "ran"
	)

	platforms := []struct {
		goos    string
		isWin   bool
		rawPath string
	}{
		{goos: "darwin", isWin: false, rawPath: unixPath},
		{goos: "linux", isWin: false, rawPath: unixPath},
		{goos: "windows", isWin: true, rawPath: winPath},
	}

	for _, p := range platforms {
		t.Run(p.goos, func(t *testing.T) {
			writeCmd := hookWriteCommandFor(p.goos, p.rawPath, text)
			stdinCmd := hookStdinCommandFor(p.goos, p.rawPath)
			pwdCmd := hookPwdCommandFor(p.goos, p.rawPath)

			cmds := []struct {
				name string
				cmd  string
			}{
				{name: "write", cmd: writeCmd},
				{name: "stdin", cmd: stdinCmd},
				{name: "pwd", cmd: pwdCmd},
			}

			for _, c := range cmds {
				if c.cmd == "" {
					t.Fatalf("%s command for %s was empty", c.name, p.goos)
				}
				if p.isWin {
					if strings.Contains(c.cmd, `\`) {
						t.Errorf("%s command for windows contains unescaped backslash, which corrupts under bash: %q", c.name, c.cmd)
					}
					wantTarget := toSlash(p.rawPath)
					if !strings.Contains(c.cmd, wantTarget) {
						t.Errorf("%s command for windows does not target %q: %q", c.name, wantTarget, c.cmd)
					}
				} else {
					if strings.Contains(c.cmd, "2>nul") || strings.Contains(c.cmd, "more") {
						t.Errorf("%s command for unix leaked windows-specific fallback syntax: %q", c.name, c.cmd)
					}
					if !strings.Contains(c.cmd, p.rawPath) {
						t.Errorf("%s command for unix does not target %q: %q", c.name, p.rawPath, c.cmd)
					}
				}
			}

			if p.isWin {
				if !strings.Contains(stdinCmd, "more") {
					t.Errorf("windows stdin command lacks cmd.exe fallback: %q", stdinCmd)
				}
				if !strings.Contains(pwdCmd, "cd") {
					t.Errorf("windows pwd command lacks cmd.exe fallback: %q", pwdCmd)
				}
			} else {
				if !strings.HasPrefix(stdinCmd, "cat >") {
					t.Errorf("unix stdin command should use POSIX cat: %q", stdinCmd)
				}
				if !strings.HasPrefix(pwdCmd, "pwd >") {
					t.Errorf("unix pwd command should use POSIX pwd: %q", pwdCmd)
				}
			}
		})
	}
}

// TestHookCommandPicksPlatformCommandForUnixAndWindows proves that the helper
// produces the appropriate command syntax for both Unix and Windows branches.
func TestHookCommandPicksPlatformCommandForUnixAndWindows(t *testing.T) {
	const (
		winPath  = `C:\Users\runneradmin\AppData\Local\Temp\TestRun001\ran`
		unixPath = `/tmp/test-run-001/ran`
		payload  = "ran"
	)

	unixWrite := hookWriteCommandFor("darwin", unixPath, payload)
	winWrite := hookWriteCommandFor("windows", winPath, payload)
	t.Logf("unix write command:    %s", unixWrite)
	t.Logf("windows write command: %s", winWrite)

	if want := "echo ran > /tmp/test-run-001/ran"; unixWrite != want {
		t.Errorf("unix write = %q, want %q", unixWrite, want)
	}
	if want := "echo ran> C:/Users/runneradmin/AppData/Local/Temp/TestRun001/ran"; winWrite != want {
		t.Errorf("windows write = %q, want %q", winWrite, want)
	}

	unixStdin := hookStdinCommandFor("darwin", unixPath)
	winStdin := hookStdinCommandFor("windows", winPath)
	t.Logf("unix stdin command:    %s", unixStdin)
	t.Logf("windows stdin command: %s", winStdin)

	if want := "cat > /tmp/test-run-001/ran"; unixStdin != want {
		t.Errorf("unix stdin = %q, want %q", unixStdin, want)
	}
	if want := "(cat 2>nul || more) > C:/Users/runneradmin/AppData/Local/Temp/TestRun001/ran"; winStdin != want {
		t.Errorf("windows stdin = %q, want %q", winStdin, want)
	}

	unixPwd := hookPwdCommandFor("darwin", unixPath)
	winPwd := hookPwdCommandFor("windows", winPath)
	t.Logf("unix pwd command:      %s", unixPwd)
	t.Logf("windows pwd command:   %s", winPwd)

	if want := "pwd > /tmp/test-run-001/ran"; unixPwd != want {
		t.Errorf("unix pwd = %q, want %q", unixPwd, want)
	}
	if want := "(pwd 2>nul || cd) > C:/Users/runneradmin/AppData/Local/Temp/TestRun001/ran"; winPwd != want {
		t.Errorf("windows pwd = %q, want %q", winPwd, want)
	}
}
