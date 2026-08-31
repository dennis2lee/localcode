package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/client"
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spinTickMsg:
		return m.handleSpinTick()

	case tea.WindowSizeMsg:
		return m.handleWindowSize(msg)

	case tea.KeyMsg:
		// A picker owns the keyboard while it is open, so nothing lands
		// in the prompt box behind a list somebody is reading.
		if m.picker != nil {
			model, cmd, _ := m.handlePickerKey(msg)
			return model, cmd
		}
		if model, cmd, handled := m.handleKey(msg); handled {
			return model, cmd
		}
		// Not handled above — fall through to the textarea below, same as
		// every other message type.

	case eventMsg:
		if msg.gen != m.streamGen {
			// From a session this client has left. Dropping it is the
			// point of the generation; re-arming the read is not, since
			// that stream is over.
			return m, nil
		}
		return m.handleServerEvent(msg)

	case streamEndedMsg:
		if msg.gen != m.streamGen {
			return m, nil
		}
		// The current stream closed. Nothing to re-arm and nothing to
		// say: the daemon going away shows up as the next call failing,
		// which reports itself.
		return m, nil

	case sessionsMsg:
		return m.handleSessionsMsg(msg)

	case archivedSessionsMsg:
		return m.handleArchivedSessionsMsg(msg)

	case sessionArchivedMsg:
		return m.handleSessionArchived(msg)

	case sessionRetrievedMsg:
		return m.handleSessionRetrieved(msg)

	case landingSessionsMsg:
		return m.handleLandingSessions(msg)

	case sessionCreatedMsg:
		return m.handleSessionCreated(msg)

	case sessionSwitchedMsg:
		return m.handleSessionSwitched(msg)

	case turnDoneMsg:
		return m.handleTurnDone(msg)

	case opErrMsg:
		if msg.err != nil {
			m.errMsg = msg.err.Error()
		}
		// On success the actual state change comes from the event the call
		// caused the daemon to broadcast (agent.switched, permission
		// resolution, turn.cancelled) — not here, so every subscribed
		// client (including this one) reacts to it the same way.
		return m, nil

	case taskOutputMsg:
		return m.handleTaskOutputMsg(msg)

	case taskCancelledMsg:
		if msg.err != nil {
			m.appendLocal(fmt.Sprintf("could not cancel %s: %v", msg.taskID, msg.err))
		} else {
			// The task.status event that follows says "cancelled"; this
			// only confirms the request was accepted, since the stop can
			// take a moment to reach a tool call.
			m.appendLocal(fmt.Sprintf("cancelling %s", msg.taskID))
		}
		return m, nil

	case versionMsg:
		if msg.err != nil {
			m.appendLocal("failed to fetch version: " + msg.err.Error())
		} else {
			m.appendLocal("localcode " + msg.version)
		}
		return m, nil

	case agentsMsg:
		if msg.err != nil {
			// Previously swallowed silently — a failed GET /api/agents left
			// Tab dead with no explanation at all.
			m.errMsg = fmt.Sprintf("failed to load agents: %v", msg.err)
		} else {
			m.agents = msg.agents
		}
		return m, nil

	case commandsMsg:
		if msg.err != nil {
			m.errMsg = fmt.Sprintf("failed to load custom commands: %v", msg.err)
		} else {
			m.commandsList = msg.commands
		}
		return m, nil

	case slashCommandsMsg:
		if msg.err == nil {
			m.slashList = msg.commands
		}
		// No error line: an older daemon has no such endpoint, and
		// completion simply offers fewer names.
		return m, nil

	case skillsMsg:
		if msg.err != nil {
			// Not an error line: skills are optional, and a daemon
			// without them is not a fault. Completion simply has fewer
			// candidates.
			return m, nil
		}
		m.skillsList = msg.skills
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.resizeLayout()
	return m, cmd
}

func (m Model) handleSpinTick() (tea.Model, tea.Cmd) {
	if !m.busy() {
		// Nothing running anymore — let this loop die. The next
		// idle->busy transition starts a fresh one.
		m.spinning = false
		return m, nil
	}
	m.spin++
	return m, spinTick()
}

func (m Model) handleServerEvent(msg eventMsg) (tea.Model, tea.Cmd) {
	m.applyEvent(msg.ev)
	cmds := []tea.Cmd{listenForEvent(m.events, m.streamGen)}
	if cmd := m.dequeue(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	// An event can make us busy without a keypress (a background task
	// spawned, a queued prompt just went out) — make sure the indicator
	// animates then too. startSpin is a no-op when a loop is already
	// running.
	if m.busy() {
		if cmd := m.startSpin(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return m, tea.Batch(cmds...)
}

func (m Model) handleTurnDone(msg turnDoneMsg) (tea.Model, tea.Cmd) {
	if client.IsBusy(msg.err) {
		// The daemon already has a turn running (typed during a race
		// window, or another client's turn). That is queue material, not
		// an error: put it back at the front and wait for the running
		// turn's turn.done to drain it.
		m.queue = append([]string{msg.text}, m.queue...)
		m.waiting = true
		m.appendLocal(fmt.Sprintf("[queued] %s", msg.text))
		return m, m.startSpin()
	}
	if msg.err != nil {
		m.waiting = false
		m.errMsg = msg.err.Error()
		// Nothing took the prompt, so its echo is a lie: remove it rather
		// than leaving a line that says it was sent above the reason it
		// was not.
		m.resolvePendingUser(msg.text)
		if cmd := m.dequeue(); cmd != nil {
			return m, cmd
		}
	}
	// On success we leave m.waiting as-is: the daemon accepted the message
	// and is streaming the actual turn via events; waiting clears when a
	// turn.done/turn.cancelled/error event arrives.
	return m, nil
}

func (m Model) handleTaskOutputMsg(msg taskOutputMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.appendLocal(fmt.Sprintf("failed to fetch %s: %v", msg.taskID, msg.err))
		return m, nil
	}
	t := m.tasks[msg.taskID]
	header := fmt.Sprintf("--- %s [%s] %s ---", msg.taskID, t.status, t.prompt)
	out := msg.output
	if strings.TrimSpace(out) == "" {
		out = "(no output yet)"
	}
	m.appendLocal(header + "\n" + out)
	return m, nil
}

// handleSessionsMsg turns a session listing into the picker, or says why
// there is nothing to show.
func (m Model) handleSessionsMsg(msg sessionsMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.errMsg = fmt.Sprintf("failed to list sessions: %v", msg.err)
		return m, nil
	}
	items := make([]pickerItem, 0, len(msg.sessions))
	for _, s := range msg.sessions {
		label := s.Title
		if label == "" {
			label = s.ID
		}
		if s.ID == m.sessionID {
			label += "  (current)"
		}
		detail := s.Agent + ", " + s.CreatedAt.Local().Format("2006-01-02 15:04")
		items = append(items, pickerItem{id: s.ID, label: label, detail: detail})
	}
	cmd := m.openPicker(&picker{
		title:  "Sessions",
		items:  items,
		onPick: func(m *Model, it pickerItem) tea.Cmd { return m.openSession(it.id) },
	}, "No sessions to switch to.")
	return m, cmd
}

// openSession leaves this conversation for another one.
//
// The switch is done here rather than in a tea.Cmd because half of it is
// local: ending the old stream and clearing what belonged to the old
// session have to happen before anything from the new one can arrive.
// Only the part that talks to the daemon is deferred.
func (m *Model) openSession(id string) tea.Cmd {
	if id == m.sessionID {
		m.appendLocal("Already in this session.")
		return nil
	}
	if m.streamCancel != nil {
		m.streamCancel()
		m.streamCancel = nil
	}
	m.streamGen++
	gen := m.streamGen
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		callCtx, callCancel := context.WithTimeout(ctx, apiCallTimeout)
		defer callCancel()
		sessions, err := c.ListSessions(callCtx)
		if err != nil {
			cancel()
			return sessionSwitchedMsg{gen: gen, err: err}
		}
		agent := ""
		found := false
		for _, s := range sessions {
			if s.ID == id {
				agent, found = s.Agent, true
				break
			}
		}
		if !found {
			cancel()
			return sessionSwitchedMsg{gen: gen, err: fmt.Errorf("session %s is gone", id)}
		}
		// From sequence zero: the transcript is being rebuilt from
		// nothing, so the whole conversation is what it needs.
		ch := c.StreamEvents(ctx, id, 0)
		return sessionSwitchedMsg{sessionID: id, agent: agent, events: ch, cancel: cancel, gen: gen}
	}
}

// handleSessionSwitched adopts the new stream, or puts the old session
// back on screen if opening the new one failed.
func (m Model) handleSessionSwitched(msg sessionSwitchedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.streamGen {
		// A third switch overtook this one. Its stream is nobody's, so
		// it has to be closed rather than left running.
		if msg.cancel != nil {
			msg.cancel()
		}
		return m, nil
	}
	if msg.err != nil {
		m.errMsg = fmt.Sprintf("failed to open session: %v", msg.err)
		// The old stream is already cancelled, so this client is now
		// attached to nothing. Re-open what it had rather than leaving a
		// window that has stopped receiving.
		return m, m.reopenCurrent()
	}

	if msg.reattach {
		// The same session, a fresh stream. Only the plumbing changes.
		m.events = msg.events
		m.streamCancel = msg.cancel
		return m, listenForEvent(m.events, m.streamGen)
	}

	// Everything below belonged to the conversation being left.
	m.sessionID = msg.sessionID
	m.currentAgent = msg.agent
	m.events = msg.events
	m.streamCancel = msg.cancel
	m.transcript = nil
	m.transcriptRev++
	m.streamOpen = false
	m.history = nil
	m.historyIdx = 0
	m.draft = ""
	m.queue = nil
	m.pending = nil
	m.pendingHintShown = false
	m.waiting = false
	m.runningTool = ""
	m.thinking = false
	m.errMsg = ""
	m.tasks = map[string]taskState{}
	m.completion = completionState{}
	m.appendLocal("Switched to session " + msg.sessionID + ".")
	m.refreshViewport()
	return m, listenForEvent(m.events, m.streamGen)
}

// reopenCurrent re-attaches to the session this client is already in,
// after a failed switch cancelled its stream.
//
// It reports itself as a switch to the session already open, which is
// what it is. The transcript is left alone: it is still this session's,
// and rebuilding it would show every line a second time.
func (m *Model) reopenCurrent() tea.Cmd {
	m.streamGen++
	gen := m.streamGen
	c, id, agent := m.client, m.sessionID, m.currentAgent
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		// Replay nothing: the transcript on screen is still this
		// session's, and a re-attach that replayed it would show every
		// line a second time. The events that matter are the ones that
		// have not happened yet.
		ch := c.StreamEvents(ctx, id, ^uint64(0))
		return sessionSwitchedMsg{sessionID: id, agent: agent, events: ch, cancel: cancel, gen: gen, reattach: true}
	}
}

// The archive.
//
// "/archive" puts this conversation away and moves off it; "/retrieve"
// offers the ones that have been put away. The picker is the same one
// "/session" uses, because they are the same gesture aimed at two lists.

func (m Model) handleArchivedSessionsMsg(msg archivedSessionsMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.errMsg = fmt.Sprintf("failed to list the archive: %v", msg.err)
		return m, nil
	}
	items := make([]pickerItem, 0, len(msg.sessions))
	for _, s := range msg.sessions {
		label := s.Title
		if label == "" {
			label = s.ID
		}
		// No "(current)" marker: the conversation you are in is never in
		// this list, which is the whole point of it.
		detail := s.Agent
		if s.ArchivedAt != nil {
			detail += ", archived " + s.ArchivedAt.Local().Format("2006-01-02 15:04")
		}
		items = append(items, pickerItem{id: s.ID, label: label, detail: detail})
	}
	cmd := m.openPicker(&picker{
		title:  "Archived",
		items:  items,
		onPick: func(m *Model, it pickerItem) tea.Cmd { return m.retrieveSession(it.id) },
	}, "No archived conversations.")
	return m, cmd
}

func (m Model) handleSessionArchived(msg sessionArchivedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		// The daemon's refusal names what is still running and says to
		// wait for it or cancel it, which is the useful part; passed
		// through rather than summarised.
		m.errMsg = fmt.Sprintf("failed to archive: %v", msg.err)
		return m, nil
	}
	m.appendLocal("Archived this conversation. It keeps everything: use /retrieve to bring it back.")
	// Where to go is decided from a list fetched now, after the archive
	// succeeded. Counting first and archiving second is an interval
	// another client can delete the fallback inside.
	return m, m.fetchLanding()
}

func (m Model) handleSessionRetrieved(msg sessionRetrievedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.errMsg = fmt.Sprintf("failed to retrieve: %v", msg.err)
		return m, nil
	}
	m.appendLocal(fmt.Sprintf("Retrieved %s. It is back in the session list; /session switches to it.", msg.id))
	return m, nil
}

func (m Model) handleLandingSessions(msg landingSessionsMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.errMsg = fmt.Sprintf("archived, but could not find another conversation to open: %v", msg.err)
		return m, nil
	}
	for _, s := range msg.sessions {
		if s.ID != m.sessionID {
			return m, m.openSession(s.ID)
		}
	}
	// Nothing left. Refusing would strand the reader: the TUI has no
	// "/new", and the pre-TUI picker is behind a restart.
	m.appendLocal("That was the only conversation, so a new one is starting.")
	return m, m.createAndOpenSession()
}

// createAndOpenSession starts a conversation and opens it. The TUI's first
// use of CreateSession: until now it only ever opened one the picker in
// cmd/localcode had already made.
//
// Two steps rather than one, because opening is what attaches the event
// stream and that is openSession's job. The created id comes back as its
// own message, which the handler turns into an open.
func (m Model) createAndOpenSession() tea.Cmd {
	return call(m.client.NewSessionFn(), func(id string, err error) tea.Msg {
		return sessionCreatedMsg{id: id, err: err}
	})
}

func (m Model) handleSessionCreated(msg sessionCreatedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.errMsg = fmt.Sprintf("failed to start a new conversation: %v", msg.err)
		return m, nil
	}
	return m, m.openSession(msg.id)
}
