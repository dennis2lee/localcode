package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"localcode/internal/events"
)

// "/mcps": which MCP servers this conversation is using.
//
// The daemon could list the servers and reconnect them, and could not
// turn one off. The only way was editing config.json and running
// "/reset-mcp", which stops it for every conversation on the machine and
// needs a file edit — a bigger answer than "this one is noisy for the
// job I am doing".
//
// Per conversation, like the permission switches and the effort. Turning
// a server off hides its tools from the next request in this
// conversation; the server stays connected, because another conversation
// may be using it and reconnecting is the expensive part.

// mcpToolsOf names the registered tools belonging to one server.
func (l *Loop) mcpToolsOf(server string) []string {
	if l.Tools == nil {
		return nil
	}
	prefix := mcpToolPrefix + server + "__"
	var out []string
	for _, name := range l.Tools.Names() {
		if strings.HasPrefix(name, prefix) {
			out = append(out, name)
		}
	}
	return out
}

// hiddenMCPTools is the tools to withhold from a turn in sessionID.
//
// Read from the store rather than pinned at turn start, for the reason
// the permission switches are: it is a switch somebody flips between
// turns, and the next request is where they expect it to land.
func (l *Loop) hiddenMCPTools(ctx context.Context) map[string]bool {
	if l.Store == nil {
		return nil
	}
	id, ok := SessionIDFromContext(ctx)
	if !ok || id == "" {
		return nil
	}
	off := l.Store.MCPOffIn(id)
	if len(off) == 0 {
		return nil
	}
	hidden := map[string]bool{}
	for server := range off {
		for _, name := range l.mcpToolsOf(server) {
			hidden[name] = true
		}
	}
	return hidden
}

// routeMCPs answers "/mcps", "/mcps on <server>" and "/mcps off <server>".
func (l *Loop) routeMCPs(sessionID, text string) (bool, error) {
	arg, ok := matchToggleCommand(text, "/mcps")
	if !ok {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})

	word, server, _ := strings.Cut(strings.TrimSpace(arg), " ")
	server = strings.TrimSpace(server)
	switch strings.ToLower(word) {
	case "":
		return true, l.replyText(sessionID, l.mcpSummary(sessionID))
	case "on", "off":
		if server == "" {
			return true, l.replyText(sessionID, "usage: /mcps "+word+" <server>\n\n"+l.mcpSummary(sessionID))
		}
		if !l.knowsMCPServer(server) {
			return true, l.replyText(sessionID, fmt.Sprintf(
				"no MCP server called %q.\n\n%s", server, l.mcpSummary(sessionID)))
		}
		if _, err := l.Store.SetMCPEnabled(sessionID, server, word == "on"); err != nil {
			return true, l.replyText(sessionID, err.Error())
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s: %s in this conversation.\n", server, map[bool]string{true: "on", false: "off"}[word == "on"])
		if word == "off" {
			// What it does and does not do, because "off" reads like
			// stopping the server.
			fmt.Fprintf(&b, "Its %d tool(s) are not offered to the next request here. The server stays connected: "+
				"another conversation may be using it, and reconnecting is the expensive part.\n",
				len(l.mcpToolsOf(server)))
		}
		b.WriteString("\n" + l.mcpSummary(sessionID))
		return true, l.replyText(sessionID, b.String())
	}
	return true, l.replyText(sessionID, "usage: /mcps, or /mcps on|off <server>")
}

func (l *Loop) knowsMCPServer(name string) bool {
	for _, s := range l.mcpStates() {
		if strings.EqualFold(s.Name, name) {
			return true
		}
	}
	return false
}

// mcpSummary is what "/mcps" alone says: every server, whether it is
// working, and whether this conversation is using it.
func (l *Loop) mcpSummary(sessionID string) string {
	states := l.mcpStates()
	if len(states) == 0 {
		return "No MCP servers are configured."
	}
	off := map[string]bool{}
	if l.Store != nil {
		off = l.Store.MCPOffIn(sessionID)
	}

	var b strings.Builder
	b.WriteString("MCP servers in this conversation:\n")
	names := make([]string, 0, len(states))
	byName := map[string]IntegrationState{}
	for _, s := range states {
		names = append(names, s.Name)
		byName[s.Name] = s
	}
	sort.Strings(names)
	for _, name := range names {
		s := byName[name]
		use := "on"
		if off[name] {
			use = "off here"
		}
		fmt.Fprintf(&b, "  %-10s %-14s %s", use, s.Status, name)
		if n := len(l.mcpToolsOf(name)); n > 0 {
			fmt.Fprintf(&b, " (%d tools)", n)
		}
		if s.Detail != "" {
			fmt.Fprintf(&b, " — %s", s.Detail)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nusage: /mcps on|off <server>. Off is per conversation and hides that server's tools " +
		"from the next request; /reset-mcp reconnects them all.")
	return b.String()
}
