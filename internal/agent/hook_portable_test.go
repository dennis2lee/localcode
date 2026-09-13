package agent

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// toSlash converts backslashes in Windows file paths to forward slashes.
//
// When shell commands are invoked on Windows runners where Git Bash is present,
// backslashes in paths like `C:\Users\...\001\where` are parsed by bash as escape
// sequences (`\001` becomes an octal escape byte rather than characters `001`).
// Converting backslashes to forward slashes ensures paths remain uncorrupted under
// bash while remaining fully supported by Windows' Win32 file APIs under cmd.exe.
func toSlash(path string) string {
	return strings.ReplaceAll(path, `\`, "/")
}

// hookWriteCommand produces a shell command string that writes a known string
// to a given path, portable across Unix shells (sh) and Windows environments.
func hookWriteCommand(path, text string) string {
	return hookWriteCommandFor(runtime.GOOS, path, text)
}

func hookWriteCommandFor(goos, path, text string) string {
	if goos == "windows" {
		return fmt.Sprintf("echo %s> %s", text, toSlash(path))
	}
	return fmt.Sprintf("echo %s > %s", text, path)
}

// hookAppendStdinCommand produces a shell command string that appends stdin to
// a file at path, portable across Unix sh (`cat >>`) and Windows cmd/bash.
func hookAppendStdinCommand(path string) string {
	return hookAppendStdinCommandFor(runtime.GOOS, path)
}

func hookAppendStdinCommandFor(goos, path string) string {
	if goos == "windows" {
		return fmt.Sprintf("(cat 2>nul || more) >> %s", toSlash(path))
	}
	return fmt.Sprintf("cat >> %s", path)
}

// hookPwdCommand produces a shell command string that records the current working
// directory to path, portable across Unix sh (`pwd`) and Windows (`pwd -W` under Git Bash
// or `cd` under cmd.exe).
//
// On Windows, bare `pwd` under Git Bash prints an MSYS2 virtual path like `/tmp/...`
// which native Windows APIs cannot resolve. `pwd -W` forces Git Bash to print the
// native Win32 path. When falling back to cmd.exe where `pwd` is not a command,
// `2>nul` suppresses the failure and `|| cd` prints the current working directory.
func hookPwdCommand(path string) string {
	return hookPwdCommandFor(runtime.GOOS, path)
}

func hookPwdCommandFor(goos, path string) string {
	if goos == "windows" {
		return fmt.Sprintf("(pwd -W 2>nul || cd) > %s", toSlash(path))
	}
	return fmt.Sprintf("pwd > %s", path)
}

// TestAgentHookCommandsArePortableAcrossOperatingSystems guards the requirement that
// agent lifecycle and tool hook commands remain portable across operating systems.
//
// It prevents regression where unescaped backslashes in Windows temp directory paths
// corrupt under bash, and ensures non-POSIX environments falling back to cmd.exe
// do not fail due to missing utilities like cat or pwd.
func TestAgentHookCommandsArePortableAcrossOperatingSystems(t *testing.T) {
	const (
		winPath  = `C:\Users\runneradmin\AppData\Local\Temp\TestAgent001\hook.log`
		unixPath = `/tmp/test-agent-001/hook.log`
		text     = "done"
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
			appendCmd := hookAppendStdinCommandFor(p.goos, p.rawPath)
			pwdCmd := hookPwdCommandFor(p.goos, p.rawPath)

			cmds := []struct {
				name string
				cmd  string
			}{
				{name: "write", cmd: writeCmd},
				{name: "append", cmd: appendCmd},
				{name: "pwd", cmd: pwdCmd},
			}

			for _, c := range cmds {
				if c.cmd == "" {
					t.Fatalf("%s command for %s was empty", c.name, p.goos)
				}
				if p.isWin {
					if strings.Contains(c.cmd, `\`) {
						t.Errorf("%s command for windows contains unescaped backslash: %q", c.name, c.cmd)
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
				if !strings.Contains(appendCmd, "more") {
					t.Errorf("windows append command lacks cmd.exe fallback: %q", appendCmd)
				}
				if !strings.Contains(pwdCmd, "cd") {
					t.Errorf("windows pwd command lacks cmd.exe fallback: %q", pwdCmd)
				}
				if !strings.Contains(pwdCmd, "pwd -W") {
					t.Errorf("windows pwd command lacks pwd -W for Git Bash: %q", pwdCmd)
				}
			} else {
				if !strings.HasPrefix(appendCmd, "cat >>") {
					t.Errorf("unix append command should use POSIX cat: %q", appendCmd)
				}
				if !strings.HasPrefix(pwdCmd, "pwd >") {
					t.Errorf("unix pwd command should use POSIX pwd: %q", pwdCmd)
				}
			}
		})
	}
}

// TestAgentHookCommandPicksPlatformCommandForUnixAndWindows proves that the helper
// produces the appropriate command syntax for both Unix and Windows branches.
func TestAgentHookCommandPicksPlatformCommandForUnixAndWindows(t *testing.T) {
	const (
		winPath  = `C:\Users\runneradmin\AppData\Local\Temp\TestAgent001\hook.log`
		unixPath = `/tmp/test-agent-001/hook.log`
		payload  = "done"
	)

	unixWrite := hookWriteCommandFor("darwin", unixPath, payload)
	winWrite := hookWriteCommandFor("windows", winPath, payload)
	t.Logf("unix write command:     %s", unixWrite)
	t.Logf("windows write command:  %s", winWrite)

	if want := "echo done > /tmp/test-agent-001/hook.log"; unixWrite != want {
		t.Errorf("unix write = %q, want %q", unixWrite, want)
	}
	if want := "echo done> C:/Users/runneradmin/AppData/Local/Temp/TestAgent001/hook.log"; winWrite != want {
		t.Errorf("windows write = %q, want %q", winWrite, want)
	}

	unixAppend := hookAppendStdinCommandFor("darwin", unixPath)
	winAppend := hookAppendStdinCommandFor("windows", winPath)
	t.Logf("unix append command:    %s", unixAppend)
	t.Logf("windows append command: %s", winAppend)

	if want := "cat >> /tmp/test-agent-001/hook.log"; unixAppend != want {
		t.Errorf("unix append = %q, want %q", unixAppend, want)
	}
	if want := "(cat 2>nul || more) >> C:/Users/runneradmin/AppData/Local/Temp/TestAgent001/hook.log"; winAppend != want {
		t.Errorf("windows append = %q, want %q", winAppend, want)
	}

	unixPwd := hookPwdCommandFor("darwin", unixPath)
	winPwd := hookPwdCommandFor("windows", winPath)
	t.Logf("unix pwd command:       %s", unixPwd)
	t.Logf("windows pwd command:    %s", winPwd)

	if want := "pwd > /tmp/test-agent-001/hook.log"; unixPwd != want {
		t.Errorf("unix pwd = %q, want %q", unixPwd, want)
	}
	if want := "(pwd -W 2>nul || cd) > C:/Users/runneradmin/AppData/Local/Temp/TestAgent001/hook.log"; winPwd != want {
		t.Errorf("windows pwd = %q, want %q", winPwd, want)
	}
}
