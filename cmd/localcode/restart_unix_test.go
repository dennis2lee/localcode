//go:build !windows

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// execSelf has to replace this process rather than start one beside it.
//
// That distinction is the whole reason the update path uses exec: the new
// program keeps the process id, the terminal, the standard streams and the
// listening socket's fate, so a TUI restarted by an update comes back in
// the terminal it was in. A spawn-and-exit would look almost the same from
// a unit test and quite different from a chair.
//
// Checked by running the test binary as its own child and letting it call
// execSelf once: an exec that happened prints the marker twice from one
// process, and one that silently did nothing prints it once.
func TestExecSelfReplacesThisProcess(t *testing.T) {
	if os.Getenv("LC_EXEC_CHILD") == "" {
		cmd := exec.Command(os.Args[0], "-test.run=TestExecSelfReplacesThisProcess")
		cmd.Env = append(os.Environ(), "LC_EXEC_CHILD=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("child: %v\n%s", err, out)
		}
		if n := strings.Count(string(out), "EXECSELF-ROUND"); n != 2 {
			t.Errorf("the marker appeared %d times, want 2 — the exec did not happen:\n%s", n, out)
		}
		return
	}
	println("EXECSELF-ROUND")
	// The exec'd image runs this test again from the top. The variable is
	// what stops it going round forever, and it survives because execSelf
	// passes the environment through — which is itself part of what is
	// being checked: the restarted localcode has to keep the environment
	// it was started with.
	if os.Getenv("LC_EXEC_DONE") != "" {
		return
	}
	os.Setenv("LC_EXEC_DONE", "1")
	if err := execSelf(); err != nil {
		t.Fatalf("execSelf: %v", err)
	}
	t.Fatal("execSelf returned although it reported no error, so nothing was replaced")
}
