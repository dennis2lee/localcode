package tui

import (
	"strings"
	"testing"

	"localcode/internal/agent"
	"localcode/internal/client"
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
// test sees what the user sees.
//
// The drift it was written for is gone: the daemon half of the help is
// rendered from the list the daemon serves rather than from a paragraph
// kept in this package, so there is no second copy left to fall behind.
// The test stays because the rendering is still code — a loop that
// skipped a field, or a client that never fetched the list, would show a
// help with commands missing from it, and that is the thing a reader of
// /help would notice and nobody else would.
func TestEveryDaemonCommandIsNamedInTheHelp(t *testing.T) {
	m := newTestModel()
	// What the client has fetched is what it can show, so the fixture
	// hands it the daemon's own list — which is also the assertion: a
	// client with an empty list must not print a help that looks
	// complete.
	for _, c := range agent.SlashCommands() {
		m.slashList = append(m.slashList, client.SlashCommandInfo{
			Name: c.Name, Description: c.Description, Usage: c.Usage,
		})
	}
	help := m.renderHelp()

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
