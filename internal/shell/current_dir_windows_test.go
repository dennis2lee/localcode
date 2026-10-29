package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A git.bat in the working directory does not run in place of git.
//
// The first half shows the premise on this machine: cmd.exe started
// without the variable runs the planted file. If it stops doing that,
// this test has nothing left to show, and says so.
func TestCmdDoesNotRunAGitBatFromTheWorkingDirectory(t *testing.T) {
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = "cmd"
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git.bat"), []byte("@echo planted\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	bare := exec.Command(comspec, "/c", "git --version")
	bare.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToLower(kv), "nodefaultcurrentdirectoryinexepath=") {
			bare.Env = append(bare.Env, kv)
		}
	}
	out, _ := bare.CombinedOutput()
	if !strings.Contains(string(out), "planted") {
		t.Fatalf("cmd.exe without NoDefaultCurrentDirectoryInExePath did not run the git.bat in its directory, so this test shows nothing; output: %s", out)
	}

	Configure(comspec)
	defer Configure("")
	cmd := Command(context.Background(), "git --version")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if strings.Contains(string(out), "planted") {
		t.Errorf("shell.Command under %s ran the git.bat in the working directory: %s", comspec, out)
	}
	if err != nil || !strings.Contains(string(out), "git version") {
		t.Errorf("shell.Command under %s did not run git from PATH: err=%v output: %s", comspec, err, out)
	}
}
