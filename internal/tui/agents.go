package tui

// nextAgent returns the agent after currentAgent in m.agents, cycling
// back to the start — the Tab-key behavior. Returns "", false if there's
// nothing to cycle to (0 or 1 known agents).
func (m Model) nextAgent() (string, bool) {
	if len(m.agents) < 2 {
		return "", false
	}
	for i, a := range m.agents {
		if a.Name == m.currentAgent {
			return m.agents[(i+1)%len(m.agents)].Name, true
		}
	}
	// Current agent isn't in the known list (shouldn't normally happen) —
	// just start from the first one.
	return m.agents[0].Name, true
}

// currentModel returns the model ID m.currentAgent's profile resolves to
// (e.g. "us.anthropic.claude-sonnet-4-6"), for display in the footer.
// Returns "", false if the current agent isn't in the known list yet
// (e.g. GET /api/agents hasn't come back) or its profile has no model set.
func (m Model) currentModel() (string, bool) {
	for _, a := range m.agents {
		if a.Name == m.currentAgent {
			return a.Model, a.Model != ""
		}
	}
	return "", false
}

// agentsSummary renders the /agent listing: every known agent's name and
// description, with the current one called out.
func (m Model) agentsSummary() string {
	items := make([]namedItem, len(m.agents))
	for i, a := range m.agents {
		items[i] = namedItem{name: a.Name, desc: a.Description}
	}
	header := "Available agents (/agent <name> to switch, current: " + m.currentAgent + "):"
	return renderList(header, "No agents registered.", items)
}

// commandsSummary renders the /commands listing: every custom command
// loaded from .localcode/commands/*.md.
func (m Model) commandsSummary() string {
	items := make([]namedItem, len(m.commandsList))
	for i, c := range m.commandsList {
		items[i] = namedItem{name: "/" + c.Name, desc: c.Description}
	}
	return renderList("Available custom commands:", "No custom commands registered. (add one under .localcode/commands/*.md)", items)
}
