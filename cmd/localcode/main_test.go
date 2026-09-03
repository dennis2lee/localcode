package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A word that is neither a flag nor a subcommand used to start the agent
// as if nothing had been typed: flag.Parse ignores leftovers, so a typo —
// or a subcommand this build is too old to have — brought up the TUI with
// nothing on screen to say the command had not run. Someone waiting for
// the output of "localcode mcp list" instead got a chat prompt.
func TestAnUnknownCommandIsRefusedRatherThanStartingTheAgent(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("needs the go tool to build the binary under test")
	}
	bin := filepath.Join(t.TempDir(), "localcode"+exeSuffix)
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	out, err := exec.Command(bin, "mpc", "list").CombinedOutput()
	if err == nil {
		t.Errorf("a typo exited 0; it must not be mistaken for success:\n%s", out)
	}
	got := string(out)
	if !strings.Contains(got, `unknown command "mpc"`) {
		t.Errorf("does not name what was not understood:\n%s", got)
	}
	// And it points at what does exist, since the reason someone is here
	// is that they do not know which word was wrong.
	for _, name := range []string{"login", "mcp", "version"} {
		if !strings.Contains(got, name) {
			t.Errorf("the list of subcommands omits %q:\n%s", name, got)
		}
	}
	if strings.Contains(got, "localcode  ") {
		t.Errorf("the agent's startup banner appeared; the TUI was still started:\n%s", got)
	}
}
