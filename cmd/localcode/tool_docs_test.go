package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every tool the daemon registers has to be in docs/USAGE.md.
//
// The slash commands have had this guard since the release that found
// one TUI command and eight Web UI commands missing from the help. The
// tools never got the same one, and the gap showed: update_plan and
// ask_user reached a release documented only because whoever added them
// remembered to. A tool the model can call and the docs never mention is
// a capability nobody can find out about without reading the source.
//
// Checked against the daemon's roster rather than the one-shot's,
// because that is the larger set: leftOutOfARun names what a run drops
// and why, and all of it still exists.
func TestEveryToolIsDocumented(t *testing.T) {
	usage, err := os.ReadFile(filepath.Join("..", "..", "docs", "USAGE.md"))
	if err != nil {
		t.Fatalf("read USAGE.md: %v", err)
	}
	text := string(usage)

	f := &fakeModel{}
	smartHome(t, f.server(t).URL, smartOn)
	d, stop, err := buildDaemon(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("build the daemon: %v", err)
	}
	defer stop()

	names := d.Loop.Tools.Names()
	if len(names) < 8 {
		t.Fatalf("only %d tools registered, so this test is checking almost nothing", len(names))
	}

	var missing []string
	for _, name := range names {
		// Backticked, the way every other tool appears in the tables, so
		// a tool named after an ordinary English word cannot pass on a
		// coincidence in prose.
		if !strings.Contains(text, "`"+name+"`") {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("the daemon registers %v and docs/USAGE.md never names them in backticks. "+
			"Add each to the tool table with what it does and when it is offered.", missing)
	}
}
