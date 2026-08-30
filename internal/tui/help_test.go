package tui

import (
	"strings"
	"testing"

	"localcode/internal/agent"
)

// A command the daemon answers and this client's own help never names is
// a command only the people who wrote it know about.
//
// This drift is a matter of record rather than a hypothetical: /debate
// shipped in v0.68.0 and was added to this help the following day, in a
// commit whose message says "a command that exists is only discoverable
// in one client until this line exists in the other". Eight releases had
// already gone out with commands missing from one help text or the other,
// and every release gate passed over them, because nothing compared the
// two lists.
//
// renderHelp is called rather than the source file being read, so the
// test sees what the user sees: both the local command table and
// serverSideHelpText, in the form they are printed.
func TestEveryDaemonCommandIsNamedInTheHelp(t *testing.T) {
	help := renderHelp()

	cmds := agent.SlashCommands()
	if len(cmds) < 5 {
		t.Fatalf("only %d slash commands listed, so this test is checking almost nothing", len(cmds))
	}
	for _, c := range cmds {
		if !strings.Contains(help, "/"+c.Name) {
			t.Errorf("the daemon answers /%s and /help never names it", c.Name)
		}
	}
}
