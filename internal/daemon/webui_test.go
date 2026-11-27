package daemon

import (
	"os"
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
		t.Skip("node is not installed — skipping the Web UI tests (run them with: make test-js)")
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

	// --experimental-vm-modules: harness.js evaluates the real js/*.js files
	// as native ES modules via node:vm's SourceTextModule, which is gated
	// behind this flag. See test/webui/harness.js for why (per-test module
	// graph isolation that Node's own import() cache can't give us without a
	// cache-busting hack across every relative import, not just the entry).
	args := append([]string{"--experimental-vm-modules", "--test"}, files...)
	cmd := exec.Command(node, args...)
	childproc.Hide(cmd)
	// The gate runs this suite three times at once: its own js check, and
	// this test in the race and plain lanes. ask_answer_orders walks 2076
	// orders to its default depth of 4 on one core: 47s here, as much CPU
	// as the rest of the suite, and the suite's wall time goes from 16s to
	// 77s. Under that load on the macOS CI runner the plain lane passed its
	// 10m bound with the walk still running. Here it walks to depth 3 (311
	// orders, 5s); the js check keeps depth 4. A depth set by the caller is
	// kept.
	cmd.Env = os.Environ()
	if os.Getenv("ASK_ORDERS_DEPTH") == "" {
		cmd.Env = append(cmd.Env, "ASK_ORDERS_DEPTH=3")
	}
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
