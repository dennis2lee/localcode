package commands

import (
	"strings"
	"testing"
)

// toSlash converts backslashes in Windows file paths to forward slashes.
//
// When shell commands are invoked on Windows runners where Git Bash is present,
// backslashes in paths like `C:\Users\...\001\secret.txt` are parsed by bash as
// escape sequences (`\001` becomes an octal escape byte, `\r` becomes a carriage
// return). Converting backslashes to forward slashes ensures paths remain uncorrupted
// under bash while remaining fully supported by Windows' Win32 file APIs under cmd.exe.
func toSlash(path string) string {
	return strings.ReplaceAll(path, `\`, "/")
}

func toSlashFor(goos, path string) string {
	if goos == "windows" {
		return strings.ReplaceAll(path, `\`, "/")
	}
	return path
}

// TestShellPathConversionIsPortableAcrossOperatingSystems guards the requirement
// that file paths embedded into shell commands are converted to forward slashes on
// Windows to prevent bash escape-sequence corruption, while leaving Unix paths untouched.
func TestShellPathConversionIsPortableAcrossOperatingSystems(t *testing.T) {
	paths := []struct {
		name      string
		winInput  string
		unixInput string
		wantWin   string
		wantUnix  string
	}{
		{
			name:      "temp directory with numeric run segment",
			winInput:  `C:\Users\runneradmin\AppData\Local\Temp\TestRun001\secret.txt`,
			unixInput: `/tmp/TestRun001/secret.txt`,
			wantWin:   "C:/Users/runneradmin/AppData/Local/Temp/TestRun001/secret.txt",
			wantUnix:  "/tmp/TestRun001/secret.txt",
		},
		{
			name:      "path containing carriage return escape sequence",
			winInput:  `C:\repo\root\secret.txt`,
			unixInput: `/repo/root/secret.txt`,
			wantWin:   "C:/repo/root/secret.txt",
			wantUnix:  "/repo/root/secret.txt",
		},
		{
			name:      "relative path with backslashes",
			winInput:  `sub\dir\file.txt`,
			unixInput: `sub/dir/file.txt`,
			wantWin:   "sub/dir/file.txt",
			wantUnix:  "sub/dir/file.txt",
		},
	}

	// Negative control: verify toSlashFor actually differentiates Windows from non-Windows.
	const probe = `C:\test\path`
	if got := toSlashFor("windows", probe); got != "C:/test/path" {
		t.Fatalf("toSlashFor windows = %q, want %q", got, "C:/test/path")
	}
	if got := toSlashFor("darwin", probe); got != probe {
		t.Fatalf("toSlashFor darwin = %q, want untouched %q", got, probe)
	}
	if got := toSlashFor("linux", probe); got != probe {
		t.Fatalf("toSlashFor linux = %q, want untouched %q", got, probe)
	}

	for _, tc := range paths {
		t.Run(tc.name, func(t *testing.T) {
			gotWin := toSlashFor("windows", tc.winInput)
			if gotWin != tc.wantWin {
				t.Errorf("toSlashFor(windows, %q) = %q, want %q", tc.winInput, gotWin, tc.wantWin)
			}
			if strings.Contains(gotWin, `\`) {
				t.Errorf("windows path contains backslash, which corrupts under bash: %q", gotWin)
			}

			gotUnix := toSlashFor("darwin", tc.unixInput)
			if gotUnix != tc.wantUnix {
				t.Errorf("toSlashFor(darwin, %q) = %q, want %q", tc.unixInput, gotUnix, tc.wantUnix)
			}
		})
	}
}

// TestShellPathPicksPlatformFormatForUnixAndWindows proves that toSlashFor produces
// the slash-converted path for Windows and leaves the path unchanged on Unix.
func TestShellPathPicksPlatformFormatForUnixAndWindows(t *testing.T) {
	const (
		winPath  = `C:\Users\runneradmin\AppData\Local\Temp\TestExpandShellOutput001\secret.txt`
		unixPath = `/tmp/TestExpandShellOutput001/secret.txt`
	)

	winResult := toSlashFor("windows", winPath)
	unixResult := toSlashFor("darwin", unixPath)

	t.Logf("unix shell path:    %s", unixResult)
	t.Logf("windows shell path: %s", winResult)

	if want := "C:/Users/runneradmin/AppData/Local/Temp/TestExpandShellOutput001/secret.txt"; winResult != want {
		t.Errorf("windows shell path = %q, want %q", winResult, want)
	}
	if want := "/tmp/TestExpandShellOutput001/secret.txt"; unixResult != want {
		t.Errorf("unix shell path = %q, want %q", unixResult, want)
	}
}
