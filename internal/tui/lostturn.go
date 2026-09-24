package tui

import tea "charm.land/bubbletea/v2"

// A turn lost while the stream was down.
//
// The stream reconnects on its own (client.StreamEvents), and when the
// daemon behind it is a new process (a restart, a crash, an update that
// did not finish its handoff) the turn this client was waiting on is
// one that process never ran. Nothing will ever end it: no turn.done
// comes, the busy line says "working… esc to cancel" for good, Esc
// answers that nothing is running, and a reasoning block keeps its
// clock. The Web UI has asked the daemon since v0.104.0; this is the
// terminal's half.
//
// Two steps, each after a grace (lostTurnGrace), and every step checks
// that nothing has moved since it began: the same stream, the same
// session, no prompt sent by this client (turnEpoch), and the turn still
// in progress. The first step waits for the backlog the reconnect
// brought, which holds the turn.done of a turn that simply finished
// while the stream was away, and then asks whether the session is busy.
// The second runs only on an idle answer, and waits once more before it
// declares anything: the daemon clears a session's busy flag just before
// it writes turn.done, so an idle answer can overtake the end of a turn
// that did finish. From the question on, a turn boundary on the stream
// (turnMarks: a prompt from anywhere, a turn ending) also stands the
// check down, since the answer may describe the turn before it; before
// the question it does not, because the backlog being replayed can hold
// the lost turn's own prompt.

// turnInProgress is whether there is a turn to check: one this client
// is waiting on, or a reasoning block still streaming in one it is only
// watching.
func (m *Model) turnInProgress() bool {
	return m.waiting || m.liveThinking() >= 0
}

// stillCurrent reports whether a step begun for sessionID, gen and
// epoch still applies.
func (m *Model) stillCurrent(sessionID string, gen, epoch uint64) bool {
	return sessionID == m.sessionID && gen == m.streamGen && epoch == m.turnEpoch && m.turnInProgress()
}

func (m Model) handleLostTurnDue(msg lostTurnDueMsg) (tea.Model, tea.Cmd) {
	if !m.stillCurrent(msg.sessionID, msg.gen, msg.epoch) {
		return m, nil
	}
	if !msg.confirm {
		return m, m.checkTurn(msg)
	}
	if msg.marks != m.turnMarks {
		return m, nil
	}
	m.endLostTurn()
	return m, nil
}

func (m Model) handleTurnCheck(msg turnCheckMsg) (tea.Model, tea.Cmd) {
	// Only the send and the stream checked here: a turn boundary since
	// the question is the confirm step's to see, which compares against
	// the moment the question was asked.
	if !m.stillCurrent(msg.sessionID, msg.gen, msg.epoch) {
		return m, nil
	}
	// A failed question, a session the daemon does not know, or a busy
	// one: nothing is known to be lost, so nothing is declared.
	if msg.err != nil || !msg.found || msg.busy {
		return m, nil
	}
	return m, scheduleLostTurnConfirm(msg)
}

// endLostTurn ends a turn the daemon is not running. A turn this client
// was waiting on gets what a cancelled one gets: the prompts sent into
// it and the ones queued behind it are marked as never handed to
// anybody, and dropped rather than sent now, the way a stop drops them;
// a permission question or a model's question it left open is put away
// (nothing will resolve them, and an open one holds Enter); and a line
// says the turn did not finish. A turn it was only watching has its
// reasoning block folded, and nothing said: it was not this client's
// turn.
func (m *Model) endLostTurn() {
	waited := m.waiting
	m.endTurn()
	if waited {
		m.queue = nil
		m.abandonPendingUsers()
		m.pending = nil
		m.pendingQueue = nil
		m.asking = nil
		m.appendTool("[the localcode running this turn is no longer running it; the turn did not finish]")
	}
	m.refreshViewport()
}
