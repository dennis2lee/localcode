package tui

import tea "charm.land/bubbletea/v2"

// handleKey is the tea.KeyMsg case of Update. Most keys either return
// immediately or fall out to the bottom of Update, which forwards the
// keypress to the textarea — "up"/"down" at a history boundary and "y/n/s/a"
// with no pending permission are the two that deliberately fall through
// (returning a zero tea.Cmd here is what signals "let the caller pass this
// on"), so typing the letter y or moving within a multi-line prompt still
// works normally.
func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit, true

	case "esc":
		// Esc stops whatever is running. Queued prompts go with it: the
		// whole point of cancelling is to stop, so letting the queue
		// immediately fire the next message would be the opposite of what
		// was asked for.
		// Sent whether or not this client thinks a turn is running.
		// It often is when m.waiting is false: attaching to a session
		// mid-turn never sets it, and another client's turn.done clears
		// it. Esc then did nothing at all while output streamed in, with
		// the "esc to cancel" hint hidden too. The daemon answers
		// "nothing was running" harmlessly when there is nothing to stop.
		m.queue = nil
		return m, m.cancelTurn(), true

	case "y", "n", "s", "a":
		if m.pending != nil && m.canAnswerPermission() {
			id := m.pending.id
			canAlways := m.pending.canAlways
			m.pending = nil
			m.pendingHintShown = false
			switch msg.String() {
			case "n":
				return m, m.resolvePermission(id, false, ""), true
			case "s":
				return m, m.resolvePermission(id, true, "session"), true
			case "a":
				if !canAlways {
					return m, nil, true // no config.json to write to; "a" isn't offered
				}
				return m, m.resolvePermission(id, true, "always"), true
			default: // "y"
				return m, m.resolvePermission(id, true, ""), true
			}
		}
		// Either there is no pending request, or answering is not armed
		// yet / the prompt box has text in it. Let the letter reach the
		// textarea, which is what someone typing wanted from it.
		return m, nil, false

	case "up":
		// Recall only when the cursor can't move any further up inside the
		// box, so Up still navigates a multi-line prompt normally and only
		// reaches for history at the boundary.
		if m.pending == nil && m.atInputTop() && m.historyPrev() {
			return m, nil, true
		}
		return m, nil, false

	case "down":
		if m.pending == nil && m.atInputBottom() && m.historyNext() {
			return m, nil, true
		}
		return m, nil, false

	case "tab":
		if next, ok := m.nextAgent(); ok {
			return m, m.switchAgent(next), true
		}
		return m, nil, true

	case "enter":
		model, cmd := handleEnter(m)
		return model, cmd, true
	}

	return m, nil, false
}
