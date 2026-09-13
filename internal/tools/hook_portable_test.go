package tools

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// toSlash converts backslashes in Windows file paths to forward slashes.
//
// When shell commands are invoked on Windows runners where Git Bash is present,
// backslashes in paths like `C:\Users\...\001\captured` are parsed by bash as
// escape sequences (`\001` becomes an octal escape byte rather than characters `001`).
// Converting backslashes to forward slashes ensures paths remain uncorrupted under
// bash while remaining fully supported by Windows' Win32 file APIs under cmd.exe.
func toSlash(path string) string {
	return strings.ReplaceAll(path, `\`, "/")
}

func toSlashFor(goos, path string) string {
	if goos == "windows" {
		return strings.ReplaceAll(path, `\`, "/")
	}
	return path
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

// hookStdinCommand produces a shell command string that captures stdin to a file
// at path, portable across Unix sh (`cat >`) and Windows cmd/bash.
func hookStdinCommand(path string) string {
	return hookStdinCommandFor(runtime.GOOS, path)
}

func hookStdinCommandFor(goos, path string) string {
	if goos == "windows" {
		return fmt.Sprintf("(cat 2>nul || more) > %s", toSlash(path))
	}
	return fmt.Sprintf("cat > %s", path)
}

// hookBlockCommand produces a shell command that writes a marker file and exits
// with a non-zero exit code (e.g. exit 2 to signal a block).
//
// On Unix sh, `; exit 2` separates commands sequentially. Under cmd.exe, `;` is
// not a command separator and gets echoed literally or errors; `&& exit 2` runs
// exit 2 sequentially upon successful write in both cmd.exe and bash.
func hookBlockCommand(path, text string, exitCode int) string {
	return hookBlockCommandFor(runtime.GOOS, path, text, exitCode)
}

func hookBlockCommandFor(goos, path, text string, exitCode int) string {
	if goos == "windows" {
		return fmt.Sprintf("echo %s> %s && exit %d", text, toSlash(path), exitCode)
	}
	return fmt.Sprintf("echo %s > %s; exit %d", text, path, exitCode)
}

// TestToolHookCommandsArePortableAcrossOperatingSystems guards the requirement that
// tool-level hook commands and check commands remain portable across operating systems.
//
// It ensures that Windows paths never leak raw backslashes into shell strings, that
// cmd.exe fallbacks exist for stdin capture, and that exit-code signaling works
// across shells without failing on POSIX-only semicolon chaining.
func TestToolHookCommandsArePortableAcrossOperatingSystems(t *testing.T) {
	const (
		winPath  = `C:\Users\runneradmin\AppData\Local\Temp\TestTool001\marker`
		unixPath = `/tmp/test-tool-001/marker`
		text     = "ran"
	)

	// Negative control: verify toSlashFor differentiates Windows from Unix
	const probe = `C:\path\to\tool`
	if got := toSlashFor("windows", probe); got != "C:/path/to/tool" {
		t.Fatalf("toSlashFor for Windows = %q, want %q", got, "C:/path/to/tool")
	}
	if got := toSlashFor("linux", probe); got != probe {
		t.Fatalf("toSlashFor for Linux = %q, want %q", got, probe)
	}
	if got := toSlashFor("darwin", probe); got != probe {
		t.Fatalf("toSlashFor for Darwin = %q, want %q", got, probe)
	}

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
			slashPath := toSlashFor(p.goos, p.rawPath)
			if p.isWin {
				if strings.Contains(slashPath, `\`) {
					t.Errorf("windows toSlashFor path contains backslash: %q", slashPath)
				}
			} else {
				if slashPath != p.rawPath {
					t.Errorf("unix toSlashFor path modified: got %q, want %q", slashPath, p.rawPath)
				}
			}
			writeCmd := hookWriteCommandFor(p.goos, p.rawPath, text)
			stdinCmd := hookStdinCommandFor(p.goos, p.rawPath)
			blockCmd := hookBlockCommandFor(p.goos, p.rawPath, text, 2)

			cmds := []struct {
				name string
				cmd  string
			}{
				{name: "write", cmd: writeCmd},
				{name: "stdin", cmd: stdinCmd},
				{name: "block", cmd: blockCmd},
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
				if !strings.Contains(stdinCmd, "more") {
					t.Errorf("windows stdin command lacks cmd.exe fallback: %q", stdinCmd)
				}
				if !strings.Contains(blockCmd, "&& exit 2") {
					t.Errorf("windows block command should use && for exit code chaining: %q", blockCmd)
				}
			} else {
				if !strings.HasPrefix(stdinCmd, "cat >") {
					t.Errorf("unix stdin command should use POSIX cat: %q", stdinCmd)
				}
				if !strings.Contains(blockCmd, "; exit 2") {
					t.Errorf("unix block command should use semicolon exit code chaining: %q", blockCmd)
				}
			}
		})
	}
}

// TestToolHookCommandPicksPlatformCommandForUnixAndWindows proves that the helper
// produces the appropriate command syntax for both Unix and Windows branches.
func TestToolHookCommandPicksPlatformCommandForUnixAndWindows(t *testing.T) {
	const (
		winPath  = `C:\Users\runneradmin\AppData\Local\Temp\TestTool001\marker`
		unixPath = `/tmp/test-tool-001/marker`
		payload  = "ran"
	)

	unixPathSlash := toSlashFor("darwin", unixPath)
	winPathSlash := toSlashFor("windows", winPath)
	t.Logf("unix slash path:       %s", unixPathSlash)
	t.Logf("windows slash path:    %s", winPathSlash)

	if want := "C:/Users/runneradmin/AppData/Local/Temp/TestTool001/marker"; winPathSlash != want {
		t.Errorf("windows toSlashFor = %q, want %q", winPathSlash, want)
	}
	if want := "/tmp/test-tool-001/marker"; unixPathSlash != want {
		t.Errorf("unix toSlashFor = %q, want %q", unixPathSlash, want)
	}

	unixWrite := hookWriteCommandFor("darwin", unixPath, payload)
	winWrite := hookWriteCommandFor("windows", winPath, payload)
	t.Logf("unix write command:    %s", unixWrite)
	t.Logf("windows write command: %s", winWrite)

	if want := "echo ran > /tmp/test-tool-001/marker"; unixWrite != want {
		t.Errorf("unix write = %q, want %q", unixWrite, want)
	}
	if want := "echo ran> C:/Users/runneradmin/AppData/Local/Temp/TestTool001/marker"; winWrite != want {
		t.Errorf("windows write = %q, want %q", winWrite, want)
	}

	unixStdin := hookStdinCommandFor("darwin", unixPath)
	winStdin := hookStdinCommandFor("windows", winPath)
	t.Logf("unix stdin command:    %s", unixStdin)
	t.Logf("windows stdin command: %s", winStdin)

	if want := "cat > /tmp/test-tool-001/marker"; unixStdin != want {
		t.Errorf("unix stdin = %q, want %q", unixStdin, want)
	}
	if want := "(cat 2>nul || more) > C:/Users/runneradmin/AppData/Local/Temp/TestTool001/marker"; winStdin != want {
		t.Errorf("windows stdin = %q, want %q", winStdin, want)
	}

	unixBlock := hookBlockCommandFor("darwin", unixPath, payload, 2)
	winBlock := hookBlockCommandFor("windows", winPath, payload, 2)
	t.Logf("unix block command:    %s", unixBlock)
	t.Logf("windows block command: %s", winBlock)

	if want := "echo ran > /tmp/test-tool-001/marker; exit 2"; unixBlock != want {
		t.Errorf("unix block = %q, want %q", unixBlock, want)
	}
	if want := "echo ran> C:/Users/runneradmin/AppData/Local/Temp/TestTool001/marker && exit 2"; winBlock != want {
		t.Errorf("windows block = %q, want %q", winBlock, want)
	}
}
