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
//
// aliases match exactly as name does and are not listed in /help. They
// exist for the words somebody arrives already typing — "/sessions" and
// "/models" are what opencode calls these, and answering "there is no
// /models in this build" to a person who meant the picker they are
// looking at is a worse answer than opening it.
type localCommand struct {
	name     string
	aliases  []string
	takesArg bool
	help     string
	run      func(m *Model, arg string) tea.Cmd
}

// names is the command's own name followed by its aliases.
func (c localCommand) names() []string {
	return append([]string{c.name}, c.aliases...)
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
				m.appendLocal(m.renderHelp())
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
			// A picker rather than a word to remember: which levels exist
			// is a property of the model this conversation is on, so the
			// only way to type the right one is to be shown it.
			name:     "/effort-set",
			takesArg: true,
			help:     "choose how hard this model is asked to think, from the levels it tells apart",
			run: func(m *Model, arg string) tea.Cmd {
				if arg = strings.TrimSpace(arg); arg != "" {
					return m.setEffort(arg)
				}
				return m.fetchEffort(true)
			},
		},
		{
			name:     "/agent",
			aliases:  []string{"/agents"},
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
			name:     "/model",
			aliases:  []string{"/models", "/mo"},
			takesArg: true,
			help:     "pick which model answers; an agent name switches agent, a profile keeps the agent and changes the model",
			run: func(m *Model, arg string) tea.Cmd {
				if arg != "" {
					// An agent name switches agent, which is what this
					// command has always done. Anything else is asked of
					// the daemon as a profile, so "/model sonnet" works
					// without anybody having written an agent for it —
					// and the daemon's refusal names the choices.
					for _, a := range m.agents {
						if strings.EqualFold(a.Name, arg) {
							return m.switchAgent(a.Name)
						}
					}
					return m.setModel(arg)
				}
				// One round trip, because which profiles exist is the
				// daemon's answer and a config edit can change it.
				return m.fetchModel(true)
			},
		},
		{
			name:     "/session",
			aliases:  []string{"/sessions", "/resume", "/continue"},
			takesArg: true,
			help:     "pick a conversation to switch to; /session <id> switches directly",
			run: func(m *Model, arg string) tea.Cmd {
				if arg != "" {
					return m.openSession(arg)
				}
				return m.fetchSessions()
			},
		},
		{
			// Everything below was reachable only by mouse, in the other
			// client, until a parity review counted what a terminal
			// could not do: start a conversation, name one, copy one, or
			// throw one away.
			name:    "/new",
			aliases: []string{"/clear-session"},
			help:    "start a new conversation and switch to it",
			run: func(m *Model, _ string) tea.Cmd {
				return m.createAndOpenSession()
			},
		},
		{
			name:     "/rename",
			takesArg: true,
			help:     "name this conversation: /rename <title>",
			run: func(m *Model, arg string) tea.Cmd {
				if arg == "" {
					m.appendLocal("usage: /rename <title>")
					return nil
				}
				return m.renameSession(m.sessionID, arg)
			},
		},
		{
			name: "/fork",
			help: "copy this conversation into a new one and switch to the copy",
			run: func(m *Model, _ string) tea.Cmd {
				return m.forkSession(m.sessionID)
			},
		},
		{
			// No confirmation step, deliberately: /archive is the
			// reversible one and sits right here, so somebody reaching
			// for this has passed it. The reply says plainly that it
			// does not come back.
			name: "/delete",
			help: "delete this conversation for good; /archive is the one that keeps it",
			run: func(m *Model, _ string) tea.Cmd {
				return m.deleteSession(m.sessionID)
			},
		},
		{
			name:    "/exit",
			aliases: []string{"/quit", "/q"},
			help:    "leave localcode (exit, :q and Ctrl+C do the same)",
			run: func(m *Model, _ string) tea.Cmd {
				return tea.Quit
			},
		},
		{
			name: "/archive",
			help: "put this conversation away; it keeps everything and /retrieve brings it back",
			run: func(m *Model, _ string) tea.Cmd {
				return m.archiveSession(m.sessionID)
			},
		},
		{
			name:     "/retrieve",
			takesArg: true,
			help:     "bring an archived conversation back; /retrieve <id> retrieves it directly",
			run: func(m *Model, arg string) tea.Cmd {
				if arg != "" {
					return m.retrieveSession(arg)
				}
				return m.fetchArchivedSessions()
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
	for _, name := range cmd.names() {
		if strings.EqualFold(text, name) {
			return "", true
		}
		if !cmd.takesArg {
			continue
		}
		prefixLen := len(name)
		if len(text) <= prefixLen+1 {
			continue
		}
		if !strings.EqualFold(text[:prefixLen], name) || text[prefixLen] != ' ' {
			continue
		}
		return strings.TrimSpace(text[prefixLen+1:]), true
	}
	return "", false
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

// renderHelp lists every local command from the table above, plus the
// daemon-side commands documented in serverSideHelpText.
// renderHelp lists what this client can do, in two halves: the commands
// it answers itself, and the ones the daemon answers.
//
// A method rather than a function, because the second half is now read
// off the daemon's own list instead of a paragraph kept in this file.
// There used to be three copies of every command — SlashCommands, the
// constant that was here, and the Web UI's own array — and two of them
// were prose somebody had to remember to edit. Four guard-test failures
// in one afternoon are what that costs; they were the duplication
// reporting itself rather than a defence of it.
//
// A client that has not fetched the list yet says so, which is honest:
// the alternative was a hardcoded paragraph that is right until the
// daemon is a different version from the client, and this client can be
// attached to one over --server.
func (m Model) renderHelp() string {
	var b strings.Builder
	b.WriteString("Available commands:\n")
	for _, cmd := range localCommands() {
		name := cmd.name
		if cmd.takesArg {
			name += " [...]"
		}
		fmt.Fprintf(&b, "  %-20s %s\n", name, cmd.help)
	}
	if len(m.slashList) == 0 {
		b.WriteString("\n  (the daemon's own commands have not been fetched yet)\n")
		return b.String()
	}
	b.WriteString("\nAnswered by the daemon:\n")
	for _, c := range m.slashList {
		name := "/" + c.Name
		if c.Usage != "" {
			name += " " + c.Usage
		}
		fmt.Fprintf(&b, "  %-34s %s\n", name, c.Description)
	}
	return b.String()
}
