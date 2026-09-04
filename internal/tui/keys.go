package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// handleKey is the tea.KeyMsg case of Update. Most keys either return
// immediately or fall out to the bottom of Update, which forwards the
// keypress to the textarea — "up"/"down" at a history boundary and "y/n/s/a/d"
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

	case "1", "2", "3", "4":
		// One keystroke answers the model's question, but only when the
		// box is empty: a digit typed into a half-written message is a
		// digit, and answering with it would eat the keystroke and the
		// question at once.
		if m.asking != nil && strings.TrimSpace(m.input.Value()) == "" {
			if answer := m.asking.answerFor(msg.String()); answer != "" {
				id := m.asking.id
				m.asking = nil
				return m, m.answerQuestion(id, answer), true
			}
		}
		return m, nil, false

	case "y", "n", "s", "a", "d":
		if m.pending != nil && m.canAnswerPermission() {
			id := m.pending.id
			canAlways := m.pending.canAlways
			// A boundary question takes a different set of answers: see
			// pendingPermission.prompt. "d" only exists there, and "s"
			// and "a" mean something else.
			outside := m.pending.outside != ""
			key := msg.String()
			if key == "d" && !outside {
				return m, nil, false // an ordinary letter everywhere else
			}
			m.pending = nil
			m.pendingHintShown = false
			switch key {
			case "n":
				return m, m.resolvePermission(id, false, ""), true
			case "d":
				return m, m.resolvePermission(id, true, "outside-dir"), true
			case "s":
				if outside {
					return m, m.resolvePermission(id, true, "outside-all"), true
				}
				return m, m.resolvePermission(id, true, "session"), true
			case "a":
				if outside || !canAlways {
					// Not offered: a boundary question is answered by
					// place, and "always" would write a tool rule that
					// outlives the reason it was written.
					return m, nil, true
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

	// Reading back through the conversation.
	//
	// Until these existed there was no way to do it at all. The viewport
	// holds the whole transcript, but Update was never called on it, so
	// its own key bindings — the ones bubbles ships for exactly this —
	// never saw a keypress; and the frame sets AltScreen, which takes
	// away the terminal's own scrollback as well. Between them, anything
	// that had scrolled off the top was gone for the life of the process
	// while sitting in memory the whole time.
	//
	// PgUp/PgDn and shift+arrows, because those are the keys with nothing
	// else to do here. Plain Up and Down belong to history recall and the
	// prompt box, and taking them would trade a thing people do every
	// minute for one they do occasionally.
	case "pgup":
		m.viewport.PageUp()
		return m, nil, true

	case "pgdown":
		m.viewport.PageDown()
		return m, nil, true

	case "shift+up":
		m.viewport.ScrollUp(1)
		return m, nil, true

	case "shift+down":
		m.viewport.ScrollDown(1)
		return m, nil, true

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

	case "right":
		// Completion, but only where the key has nothing else to do:
		// the cursor at the end of a word. Inside one Right moves the
		// cursor, which is what it is for.
		if m.pending == nil && m.cursorCompletable() {
			if next, at, ok := m.nextCompletion(m.input.Value(), m.cursorRune()); ok {
				m.setInputAt(next, at)
				return m, nil, true
			}
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
