package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// isPlainPrompt reports whether text is an ordinary chat message rather
// than something the TUI itself intercepts (a "/"-prefixed local or
// server-side command, or exit/:q). Only plain prompts are safe to queue
// while a turn is in progress — queueing a command would mean replaying it
// as literal chat text to the model once dequeued, instead of running it.
func isPlainPrompt(text string) bool {
	lower := strings.ToLower(text)
	return !strings.HasPrefix(text, "/") && lower != "exit" && lower != ":q"
}

// dequeue sends the next queued prompt once the current turn has actually
// finished (m.waiting was just cleared) — the common case for someone who
// kept typing while the model was still streaming a reply. Returns nil if
// nothing is queued or a turn is still in progress.
func (m *Model) dequeue() tea.Cmd {
	if m.waiting || len(m.queue) == 0 {
		return nil
	}
	next := m.queue[0]
	m.queue = m.queue[1:]
	m.waiting = true
	return m.sendMessage(next)
}

// handleEnter is the "enter" key case: resolve a pending permission
// prompt, queue or reject input while a turn is running, or dispatch a
// local command / send an ordinary prompt.
func handleEnter(m Model) (tea.Model, tea.Cmd) {
	if m.pending != nil {
		// Typing an answer and hitting Enter here used to do nothing with
		// no explanation, which reads as broken — the permission line
		// below the box is easy to miss. Point at it once instead of
		// silently eating the keystroke (and not spamming it on every
		// repeat press).
		if !m.pendingHintShown {
			m.pendingHintShown = true
			m.appendLocal("Resolve the permission request above (see the keys under it) before sending a message.")
		}
		return m, nil
	}
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil
	}

	// A turn is already running: send the prompt anyway. The daemon hands
	// it to the running turn, which picks it up at its next tool call, so
	// a correction reaches the model while it is still working instead of
	// after it has finished the wrong thing. Only a foreground turn is
	// affected: background tasks run in their own child sessions.
	if m.waiting {
		m.rememberPrompt(text)
		m.input.Reset()
		m.resizeLayout()
		if lower := strings.ToLower(text); lower == "exit" || lower == ":q" {
			// exit/:q must still work while a turn is running — there was
			// previously no way to quit except Ctrl+C until the turn
			// finished.
			return m, tea.Quit
		}
		if isPlainPrompt(text) {
			// The transcript line for the message itself comes from the
			// message.user event the daemon writes once the model is
			// actually given it; until then this says it was accepted,
			// since that wait can be minutes.
			m.appendLocal(fmt.Sprintf("[sent — the model will pick this up at its next step] %s", text))
			return m, m.sendMessage(text)
		} else {
			// Commands can't be queued (replaying one later via
			// sendMessage would send it as literal chat text instead of
			// running it) — say so rather than silently discarding the
			// keystroke, which previously looked identical to a queued
			// prompt with no visible feedback at all.
			m.appendLocal(fmt.Sprintf("%s can't run while a turn is in progress — wait for it to finish, or press Esc to cancel it.", text))
		}
		return m, nil
	}

	m.rememberPrompt(text)
	m.input.Reset()
	m.resizeLayout()

	if lower := strings.ToLower(text); lower == "exit" || lower == ":q" {
		return m, tea.Quit
	}
	if cmd, ok := dispatchLocalCommand(&m, text); ok {
		return m, cmd
	}

	// A dimmed stand-in, replaced by the real line when the daemon's
	// message.user event lands (see appendPendingUser). That event is still
	// what the transcript keeps, so a resumed or replayed session shows
	// exactly what a live one did.
	m.appendPendingUser(text)
	m.waiting = true
	return m, tea.Batch(m.sendMessage(text), m.startSpin())
}
