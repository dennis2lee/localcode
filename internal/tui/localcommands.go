package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// namedItem is one row of a name/description listing — the shape /agent
// and /commands both render, pulled into renderList so the two formatting
// blocks can't drift apart the way they had before (identical Fprintf
// calls duplicated in two places).
type namedItem struct{ name, desc string }

// renderList formats header followed by one "- name: desc" line per item,
// or empty if there are none.
func renderList(header, empty string, items []namedItem) string {
	if len(items) == 0 {
		return empty
	}
	var b strings.Builder
	b.WriteString(header + "\n")
	for _, it := range items {
		fmt.Fprintf(&b, "- %s: %s\n", it.name, it.desc)
	}
	return strings.TrimRight(b.String(), "\n")
}

// localCommand is a slash command the TUI answers itself, without a model
// call. name is matched case-insensitively; a command with takesArg also
// matches "name <argument>" (the argument's original case is preserved
// even though the command name isn't case-sensitive).
type localCommand struct {
	name     string
	takesArg bool
	help     string
	run      func(m *Model, arg string) tea.Cmd
}

// localCommands is tried in order against whatever the user typed once a
// message is submitted (see dispatchLocalCommand). It is also renderHelp's
// only source for /help, /version, /agent, /commands, and /tasks — those
// four can no longer describe a command that dispatch doesn't actually
// implement, or vice versa, since both come from this one table.
//
// A function rather than a package-level var: the /help entry's run
// closure calls renderHelp, and renderHelp ranges over this table — a var
// holding this literal would make that a genuine package-level
// initialization cycle (var -> func -> var). Building the slice fresh on
// each call sidesteps it; the cost is a handful of closures allocated per
// keypress, not worth worrying about.
func localCommands() []localCommand {
	return []localCommand{
		{
			name: "/help",
			help: "show this help",
			run: func(m *Model, _ string) tea.Cmd {
				m.appendLocal(renderHelp())
				return nil
			},
		},
		{
			name: "/version",
			help: "show the daemon version",
			run: func(m *Model, _ string) tea.Cmd {
				return m.fetchVersion()
			},
		},
		{
			name:     "/agent",
			takesArg: true,
			help:     "list agents, or switch with /agent <name> (Tab also cycles through them)",
			run: func(m *Model, arg string) tea.Cmd {
				if arg == "" {
					m.appendLocal(m.agentsSummary())
					return nil
				}
				return m.switchAgent(arg)
			},
		},
		{
			name: "/commands",
			help: "list registered custom commands",
			run: func(m *Model, _ string) tea.Cmd {
				m.appendLocal(m.commandsSummary())
				return nil
			},
		},
		{
			name:     "/tasks",
			takesArg: true,
			help:     "list background tasks, show one's output with /tasks <id>, or stop one with /tasks cancel <id>",
			run: func(m *Model, arg string) tea.Cmd {
				if arg == "" {
					m.appendLocal(m.tasksSummary())
					return nil
				}
				// "cancel <id>" rather than a command of its own: a task
				// id is only ever read off this listing, so the way to
				// stop one belongs next to the way to look at one.
				if id, ok := strings.CutPrefix(arg, "cancel"); ok {
					id = strings.TrimSpace(id)
					if id == "" {
						m.appendLocal("usage: /tasks cancel <id>")
						return nil
					}
					return m.cancelTask(id)
				}
				return m.fetchTaskOutput(arg)
			},
		},
	}
}

// matchLocalCommand checks whether text invokes cmd. The command name
// matches case-insensitively (fixing a real bug: "/Agent foo" used to fall
// through to the model as ordinary chat text, because the old dispatch's
// exact-match switch lowercased its subject but the CutPrefix-based
// argument checks didn't); the argument, if any, keeps its original case,
// since a task ID or agent name is not case-insensitive.
func matchLocalCommand(text string, cmd localCommand) (arg string, ok bool) {
	if strings.EqualFold(text, cmd.name) {
		return "", true
	}
	if !cmd.takesArg {
		return "", false
	}
	prefixLen := len(cmd.name)
	if len(text) <= prefixLen+1 {
		return "", false
	}
	if !strings.EqualFold(text[:prefixLen], cmd.name) || text[prefixLen] != ' ' {
		return "", false
	}
	return strings.TrimSpace(text[prefixLen+1:]), true
}

// dispatchLocalCommand tries every entry of localCommands against text (the
// full, already-trimmed prompt) and runs the first match. ok reports
// whether anything matched at all — false means text should go to the
// model (an unmatched "/"-prefixed word, a custom command, or plain chat).
func dispatchLocalCommand(m *Model, text string) (tea.Cmd, bool) {
	for _, cmd := range localCommands() {
		if arg, ok := matchLocalCommand(text, cmd); ok {
			return cmd.run(m, arg), true
		}
	}
	return nil, false
}

// serverSideHelpText documents the slash commands SendMessage intercepts
// on the daemon (internal/agent/commands.go), which have no entry in
// localCommands since the TUI never runs them itself — it just forwards
// the text like any other prompt.
const serverSideHelpText = `  /skill              list registered skills
  /<skill name>        run that skill (e.g. /pdf-tools)
  /init              scan the repo and create/improve an AGENTS.md rules file
  /memory            show the auto memory directory/index (MEMORY.md)
  /config            show current settings (auto_compact, show_tps, auto_delegate)
  /config auto_compact on|off   toggle auto-compaction above 80% context usage
  /config show_tps on|off       toggle the tokens/sec display under the prompt
  /config auto_delegate on|off  send matching prompts to a cheaper sub-agent
  /compact           summarize and compact the conversation right now
  /compact <instructions>      give instructions for how to compact
  /usage              show cumulative token usage per model
  /<custom command>   run a command defined in .localcode/commands/*.md
  exit, :q            quit the TUI (same as Ctrl+C)

Enter to send, Ctrl+J for a newline, Tab to switch agents, Esc to cancel a running turn.`

// renderHelp lists every local command from the table above, plus the
// daemon-side commands documented in serverSideHelpText.
func renderHelp() string {
	var b strings.Builder
	b.WriteString("Available commands:\n")
	for _, cmd := range localCommands() {
		name := cmd.name
		if cmd.takesArg {
			name += " [...]"
		}
		fmt.Fprintf(&b, "  %-20s %s\n", name, cmd.help)
	}
	b.WriteString(serverSideHelpText)
	return b.String()
}
