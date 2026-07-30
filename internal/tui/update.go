package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/client"
	"localcode/internal/events"
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spinTickMsg:
		return m.handleSpinTick()

	case tea.WindowSizeMsg:
		return m.handleWindowSize(msg)

	case tea.KeyMsg:
		if model, cmd, handled := m.handleKey(msg); handled {
			return model, cmd
		}
		// Not handled above — fall through to the textarea below, same as
		// every other message type.

	case eventMsg:
		return m.handleServerEvent(msg)

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
	m.applyEvent(events.Event(msg))
	cmds := []tea.Cmd{listenForEvent(m.events)}
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
