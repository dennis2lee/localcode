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
// The array those eight were missing from is gone. The daemon half of
// the help is rendered from the list this daemon serves, so there is no
// copy left to fall behind — and what this guard checks is that, rather
// than a list of names it would now be checking against itself.
//
// Kept, and turned around, because the copy can come back. Somebody
// adding a command and wanting it in the help has a paragraph-shaped
// habit to fall into, and a hardcoded line here would work for exactly
// as long as the two versions matched.
func TestTheWebHelpRendersTheDaemonsOwnList(t *testing.T) {
	src, err := os.ReadFile("static/js/commands.js")
	if err != nil {
		t.Fatalf("read commands.js: %v", err)
	}
	body := string(src)

	// It reads the fetched list, and renders each entry's own usage and
	// description rather than text of its own.
	for _, want := range []string{"app.slashCommands", "c.usage", "c.description"} {
		if !strings.Contains(body, want) {
			t.Errorf("commands.js does not render the daemon's list (%q missing): the help would be a copy again", want)
		}
	}

	// And no daemon command is written down here. The local half names a
	// handful of client-side ones on purpose; anything the daemon answers
	// appearing as a literal is the copy coming back.
	local := map[string]bool{"help": true, "version": true, "agent": true, "commands": true}
	cmds := agent.SlashCommands()
	if len(cmds) < 5 {
		t.Fatalf("only %d slash commands listed, so this test is checking almost nothing", len(cmds))
	}
	for _, c := range cmds {
		if local[c.Name] {
			continue
		}
		if strings.Contains(body, "'  /"+c.Name) || strings.Contains(body, "'  /"+c.Name+" ") {
			t.Errorf("/%s is written into the Web UI help as a literal; it should come from the daemon's list", c.Name)
		}
	}
}

// Every command the daemon serves carries what a client needs to render
// it: a description, and the argument form where it takes one.
//
// On this side rather than in each client, which is the point of the
// field. A command with no description renders as a bare name in two
// help texts, and nobody editing this list would see either of them.
func TestEveryDaemonCommandDescribesItself(t *testing.T) {
	for _, c := range agent.SlashCommands() {
		if strings.TrimSpace(c.Description) == "" {
			t.Errorf("/%s has no description, so both clients would list it as a bare name", c.Name)
		}
		// A usage string is not required — most commands take nothing —
		// but one that is set must not repeat the name, which is the
		// mistake that renders as "/effort /effort [off|low]".
		if c.Usage != "" && strings.Contains(c.Usage, "/"+c.Name) {
			t.Errorf("/%s: usage %q repeats the command name", c.Name, c.Usage)
		}
	}
}
