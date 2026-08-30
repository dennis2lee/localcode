package daemon

import (
	"os"
	"strings"
	"testing"

	"localcode/internal/agent"
)

// The Web UI's /help has to name every command the daemon answers.
//
// The TUI has the same guard (internal/tui). This one is the reason both
// exist: when the two were compared for the first time, the TUI was one
// command short and this help text was eight — /permission-skip-tools,
// /read-outside, /write-outside, /schedule, /show-scheduled-task,
// /debate, /effort and /context, shipped across v0.57.0 to v0.71.0. Every
// one of them was in docs/USAGE.md, so the documentation gate was working
// and the drift was entirely between the program and its own help.
//
// Only the HELP_TEXT array is searched, not the whole file. Nearly every
// command name also appears in the routing code below it, so a whole-file
// search would report a command as documented on the strength of the line
// that implements it.
func TestEveryDaemonCommandIsNamedInTheWebHelp(t *testing.T) {
	src, err := os.ReadFile("static/js/commands.js")
	if err != nil {
		t.Fatalf("read commands.js: %v", err)
	}
	help, err := helpTextBlock(string(src))
	if err != nil {
		t.Fatalf("%v", err)
	}

	cmds := agent.SlashCommands()
	if len(cmds) < 5 {
		t.Fatalf("only %d slash commands listed, so this test is checking almost nothing", len(cmds))
	}
	for _, c := range cmds {
		if !strings.Contains(help, "/"+c.Name) {
			t.Errorf("the daemon answers /%s and the Web UI help never names it", c.Name)
		}
	}
}

// helpTextBlock returns the source text of the HELP_TEXT array literal.
// It fails rather than returning nothing if the array is renamed or
// reshaped, so the guard cannot go quiet by finding an empty string in
// every command.
func helpTextBlock(src string) (string, error) {
	const marker = "HELP_TEXT = ["
	i := strings.Index(src, marker)
	if i < 0 {
		return "", errNoHelpText
	}
	rest := src[i+len(marker):]
	j := strings.Index(rest, "];")
	if j < 0 {
		return "", errNoHelpText
	}
	return rest[:j], nil
}

var errNoHelpText = helpTextError("no `HELP_TEXT = [ ... ];` array in static/js/commands.js: this guard reads that array by name, so renaming or reshaping it needs this test updated rather than left to pass on nothing")

type helpTextError string

func (e helpTextError) Error() string { return string(e) }
