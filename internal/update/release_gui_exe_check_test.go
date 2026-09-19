package update

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A console build packaged under localcode-gui.exe has happened and shipped
// with no desktop window. scripts/check-gui-exe.sh is run in preflight to
// ensure the shipped artifact is genuinely a GUI build: it must reject any
// binary missing the "gui" tag or windowsgui subsystem flag.
func TestCheckGUIExeRejectsConsoleBuild(t *testing.T) {
	// Not on Windows, and the reason is what the script is for rather
	// than what will not run: check-gui-exe.sh is preflight on the machine
	// a release is cut from, and that machine builds the MSI with wixl,
	// which is macOS and Linux only. Windows never runs it. It is also
	// written for a shell that is not the one a Windows Go test can
	// assume, which is the same fact from the other side.
	if runtime.GOOS == "windows" {
		t.Skip("check-gui-exe.sh runs where a release is packaged, which is never Windows")
	}

	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "check-gui-exe.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Skipf("check-gui-exe.sh not found: %v", err)
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	version := "0.135.0"
	tmp := t.TempDir()
	exe := filepath.Join(tmp, "localcode.exe")

	buildCmd := exec.Command("go", "build",
		"-ldflags", fmt.Sprintf("-s -w -X main.version=%s", version),
		"-o", exe,
		"./cmd/localcode",
	)
	buildCmd.Dir = repoRoot
	buildCmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Skipf("cannot cross-compile windows/amd64 console build: %v\n%s", err, string(out))
	}

	checkCmd := exec.Command(script, version, exe)
	checkCmd.Dir = repoRoot
	var stderr bytes.Buffer
	checkCmd.Stderr = &stderr
	out, err := checkCmd.Output()
	if err == nil {
		t.Fatalf("check-gui-exe.sh accepted a console build; stdout:\n%s", string(out))
	}

	errText := stderr.String()
	// The refusal must be because the binary lacks the GUI build tag or
	// Windows GUI subsystem, rather than an incidental failure like a
	// missing VCS revision or version mismatch.
	if !strings.Contains(errText, "-tags=gui") && !strings.Contains(errText, "windowsgui") && !strings.Contains(errText, "subsystem") {
		t.Fatalf("refusal was not the expected tag or subsystem check:\nstderr: %s\nstdout: %s", errText, string(out))
	}
}
