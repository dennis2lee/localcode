package shell

import (
	"slices"
	"testing"
)

// cmd.exe looks in the current directory before PATH unless this is in
// its environment. Every shell command on Windows carries it; see
// noCurrentDirLookup. The Windows half, which runs a planted git.bat, is
// in current_dir_windows_test.go.
func TestWindowsShellCommandsDoNotSearchTheWorkingDirectory(t *testing.T) {
	env := commandEnv("windows", []string{"PATH=C:\\Windows"})
	if !slices.Contains(env, "NoDefaultCurrentDirectoryInExePath=1") {
		t.Errorf("windows command env = %q, want NoDefaultCurrentDirectoryInExePath=1 in it", env)
	}
	if !slices.Contains(env, "PATH=C:\\Windows") {
		t.Errorf("windows command env = %q, lost the inherited PATH", env)
	}
	for _, goos := range []string{"linux", "darwin"} {
		if env := commandEnv(goos, []string{"PATH=/usr/bin"}); env != nil {
			t.Errorf("%s command env = %q, want nil (inherit)", goos, env)
		}
	}
}
