package main

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// build/msi-checks-test.sh tests the MSI table checks that
// build/package-msi.sh runs over the installer it builds: the
// RemoveExistingProducts order and the six pinned component GUIDs.
// A test nobody runs has already shipped in this repo twice, so this
// runs it on every `make check` (through `go test ./...`), beside the
// other repository tests. Skipped on Windows, where no job runs it:
// the script is bash over awk and mktemp.
func TestMSIChecks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("msi-checks-test.sh is bash over Unix tooling, and no job on Windows runs it")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "build", "msi-checks-test.sh"))
	if err != nil {
		t.Fatalf("resolve build/msi-checks-test.sh: %v", err)
	}
	// Run by path rather than copying it elsewhere: the script finds
	// build/msi-checks.sh relative to itself.
	out, err := exec.Command("bash", script).CombinedOutput()
	if err != nil {
		t.Fatalf("build/msi-checks-test.sh: %v\n%s", err, out)
	}
}
