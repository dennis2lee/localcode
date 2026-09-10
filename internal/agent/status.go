package agent

import (
	"fmt"
	"runtime"
	"sort"
	"strings"

	"localcode/internal/events"
)

// "/status" and "/debug": what is attached, and what this build is.
//
// Both exist because a parity review found the same hole from two sides.
// An MCP server that fails to connect says so in an event the Web UI
// paints into a little indicator, and nowhere else — a terminal user had
// no way at all to see that a server was down, let alone why. And a
// person filing a bug had no way to say what they were running beyond
// reading the version off the splash.
//
// They are answered here rather than in the terminal so both clients get
// them from one place, and so the answer is the daemon's own account of
// itself rather than each client's guess.

// IntegrationState is one MCP server's line in "/status".
//
// A type of this package's own rather than mcp.ServerState, so the agent
// loop keeps no dependency on internal/mcp — the same reason ReloadMCP is
// a hook rather than a call. The daemon, which has both, adapts.
type IntegrationState struct {
	Name   string
	Status string
	// Detail is the last error, when there is one. It is the half of
	// "disconnected" somebody can act on.
	Detail string
}

// routeStatus answers "/status".
func (l *Loop) routeStatus(sessionID, text string) (bool, error) {
	if strings.TrimSpace(text) != "/status" {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})
	return true, l.replyText(sessionID, l.statusReport())
}

// statusReport is what "/status" says: everything attached to this
// daemon, and for MCP servers whether it is actually working.
func (l *Loop) statusReport() string {
	var b strings.Builder

	b.WriteString("MCP servers\n")
	states := l.mcpStates()
	switch {
	case len(states) == 0:
		b.WriteString("  none configured\n")
	default:
		for _, s := range states {
			fmt.Fprintf(&b, "  %-14s %s", s.Status, s.Name)
			if s.Detail != "" {
				fmt.Fprintf(&b, " — %s", s.Detail)
			}
			b.WriteString("\n")
		}
	}

	skills := l.SkillList()
	skillNames := make([]string, 0, len(skills))
	for _, s := range skills {
		skillNames = append(skillNames, s.Name)
	}
	sort.Strings(skillNames)
	fmt.Fprintf(&b, "\nSkills (%d)%s\n", len(skills), listSuffix(skillNames))

	cmdNames := make([]string, 0, len(l.Commands))
	for _, c := range l.Commands {
		cmdNames = append(cmdNames, c.Name)
	}
	sort.Strings(cmdNames)
	fmt.Fprintf(&b, "Custom commands (%d)%s\n", len(l.Commands), listSuffix(cmdNames))

	if agents := l.agentLines(); len(agents) > 0 {
		fmt.Fprintf(&b, "\nAgents (%d)\n", len(agents))
		for _, line := range agents {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}

	if l.ProjectDir != "" {
		fmt.Fprintf(&b, "\nWorkspace: %s\n", l.ProjectDir)
	}
	b.WriteString("\n/debug reports what this build is, for a bug report.")
	return strings.TrimRight(b.String(), "\n")
}

// routeDebug answers "/debug".
func (l *Loop) routeDebug(sessionID, agentName, text string) (bool, error) {
	if strings.TrimSpace(text) != "/debug" {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})
	return true, l.replyText(sessionID, l.debugReport(sessionID, agentName))
}

// debugReport is the block somebody pastes into a bug report: what this
// build is, what it is pointed at, and what it is talking to.
//
// Deliberately one plain block with no styling, because the whole use of
// it is being copied out of a terminal into somewhere else.
func (l *Loop) debugReport(sessionID, agentName string) string {
	profileName, profile := l.profileOrZero(agentName)

	var b strings.Builder
	b.WriteString("Paste this into a bug report.\n\n")
	fmt.Fprintf(&b, "localcode   %s\n", firstNonEmpty(l.Version, "unknown"))
	fmt.Fprintf(&b, "platform    %s/%s, %s\n", runtime.GOOS, runtime.GOARCH, runtime.Version())
	fmt.Fprintf(&b, "session     %s\n", sessionID)
	fmt.Fprintf(&b, "agent       %s\n", firstNonEmpty(profileName, "none"))
	fmt.Fprintf(&b, "model       %s\n", firstNonEmpty(modelName(profile), "none"))
	if l.Store != nil {
		if e := l.Store.EffortFor(sessionID, profile.Model); e != "" {
			fmt.Fprintf(&b, "effort      %s (this conversation)\n", e)
		} else if profile.Effort != "" {
			fmt.Fprintf(&b, "effort      %s (the %q profile)\n", profile.Effort, profileName)
		}
	}
	fmt.Fprintf(&b, "workspace   %s\n", firstNonEmpty(l.ProjectDir, "none"))
	fmt.Fprintf(&b, "config      %s\n", firstNonEmpty(l.ConfigPath, "none"))

	states := l.mcpStates()
	connected := 0
	for _, s := range states {
		if s.Status == "connected" {
			connected++
		}
	}
	fmt.Fprintf(&b, "mcp         %d configured, %d connected\n", len(states), connected)
	fmt.Fprintf(&b, "skills      %d\n", len(l.SkillList()))
	fmt.Fprintf(&b, "commands    %d\n", len(l.Commands))
	return strings.TrimRight(b.String(), "\n")
}

// mcpStates reads the hook, and reports none when the daemon never wired
// one — which is every build without MCP servers, and every test.
func (l *Loop) mcpStates() []IntegrationState {
	if l.MCPStates == nil {
		return nil
	}
	states := l.MCPStates()
	sort.Slice(states, func(i, j int) bool { return states[i].Name < states[j].Name })
	return states
}

// agentLines is "name → model" for every configured profile.
func (l *Loop) agentLines() []string {
	var out []string
	for name, p := range l.Config.Profiles {
		model := modelName(p)
		if model == "" {
			model = "no model"
		}
		out = append(out, fmt.Sprintf("%s → %s", name, model))
	}
	sort.Strings(out)
	return out
}

// listSuffix is ": a, b, c" for a short list and "" for none, so a count
// of zero does not trail an empty colon.
func listSuffix(names []string) string {
	if len(names) == 0 {
		return ""
	}
	const most = 12
	if len(names) > most {
		return fmt.Sprintf(": %s, and %d more", strings.Join(names[:most], ", "), len(names)-most)
	}
	return ": " + strings.Join(names, ", ")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
