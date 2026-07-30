package daemon

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/childproc"
)

// TestWebUI runs the JavaScript unit tests for the page this package embeds
// and serves (see test/webui/README.md). It lives here, rather than only in
// the Makefile, so `go test ./...` covers the Web UI the same way it covers
// everything else — the browser code is the one part of the program with no
// Go test in front of it.
//
// Node is not a build dependency of this project and nothing about the
// release path needs it, so a machine without it skips rather than fails.
func TestWebUI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Web UI suite in -short mode")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed — skipping the Web UI tests (run them with: node --test test/webui/)")
	}

	dir, err := filepath.Abs(filepath.Join("..", "..", "test", "webui"))
	if err != nil {
		t.Fatalf("resolve test/webui: %v", err)
	}
	// Named explicitly rather than handing node the directory: `node --test
	// <dir>` tries to load the directory as a module instead of discovering
	// the files in it.
	files, err := filepath.Glob(filepath.Join(dir, "*.test.js"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
	}
	if len(files) == 0 {
		t.Fatalf("no *.test.js files in %s — the Web UI suite has gone missing", dir)
	}

	cmd := exec.Command(node, append([]string{"--test"}, files...)...)
	childproc.Hide(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the Web UI tests failed: %v\n%s", err, out)
	}
	// node --test exits 0 with a "fail 0" summary on success; print the tail
	// on -v so a passing run still shows how many tests actually ran.
	if testing.Verbose() {
		t.Log("\n" + strings.TrimSpace(string(out)))
	}
}
