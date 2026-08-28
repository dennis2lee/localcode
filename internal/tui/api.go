package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/client"
	"localcode/internal/events"
	"localcode/internal/session"
)

// apiCallTimeout bounds every daemon call issued from here. Without it, a
// hung daemon (dropped connection, deadlocked handler) would wedge the
// tea.Cmd goroutine forever — Esc cancels a running *turn*, but nothing
// previously bounded the HTTP round trip these calls make.
const apiCallTimeout = 30 * time.Second

// call runs one client call off the Update goroutine with a timeout, and
// wraps the result in the message wrap builds. Sharing this (and callErr,
// below) is what replaced eight near-identical tea.Cmd closures that each
// hardcoded context.Background() with no timeout at all.
func call[T any](fn func(context.Context) (T, error), wrap func(T, error) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiCallTimeout)
		defer cancel()
		v, err := fn(ctx)
		return wrap(v, err)
	}
}

// callErr is call's variant for a client method that returns only an
// error (no value to carry back) — CancelTurn, ResolvePermission, and
// SendMessage's fire-and-forget shape.
func callErr(fn func(context.Context) error, wrap func(error) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), apiCallTimeout)
		defer cancel()
		return wrap(fn(ctx))
	}
}

// listenForEvent waits for one event and tags it with the generation of
// the stream it came from.
//
// The tag is what lets a session switch be clean. Switching cancels the
// old stream, but a read already parked on its channel is not cancelled
// by that: it either returns the event that was in flight or returns
// nothing when the channel closes. Both arrive after the switch, and
// without the generation the first would be shown in the new session and
// the second would look like the new stream ending.
func listenForEvent(ch <-chan events.Event, gen uint64) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return streamEndedMsg{gen: gen}
		}
		return eventMsg{ev: ev, gen: gen}
	}
}

func (m Model) sendMessage(text string) tea.Cmd {
	return callErr(func(ctx context.Context) error {
		return m.client.SendMessage(ctx, m.sessionID, text)
	}, func(err error) tea.Msg { return turnDoneMsg{text: text, err: err} })
}

// cancelTurn asks the daemon to stop the running turn. The transcript
// line and the cleared spinner come from the turn.cancelled event the
// daemon broadcasts, not from here, so every attached client reacts to a
// cancel the same way regardless of which one pressed Esc.
func (m Model) cancelTurn() tea.Cmd {
	return callErr(func(ctx context.Context) error {
		return m.client.CancelTurn(ctx, m.sessionID)
	}, func(err error) tea.Msg { return opErrMsg{op: "cancel turn", err: err} })
}

// resolvePermission answers a pending permission request. scope is
// "once" (or ""), "session", or "always" — see agent.PermissionBroker.
// The actual policy change (remembering the grant, writing it to
// config.json) happens server-side; this only reports what the user
// chose.
func (m Model) resolvePermission(id string, allow bool, scope string) tea.Cmd {
	return callErr(func(ctx context.Context) error {
		return m.client.ResolvePermission(ctx, m.sessionID, id, allow, scope)
	}, func(err error) tea.Msg { return opErrMsg{op: "resolve permission", err: err} })
}

func (m Model) fetchVersion() tea.Cmd {
	return call(m.client.Version, func(v string, err error) tea.Msg { return versionMsg{version: v, err: err} })
}

func (m Model) fetchAgents() tea.Cmd {
	return call(m.client.ListAgents, func(a []client.AgentInfo, err error) tea.Msg { return agentsMsg{agents: a, err: err} })
}

// fetchTaskOutput pulls everything a background task has produced so far
// (works mid-run — a task is a session, so its stream is readable while
// it is still going).
func (m Model) fetchTaskOutput(taskID string) tea.Cmd {
	return call(func(ctx context.Context) (string, error) {
		return m.client.TaskOutput(ctx, taskID)
	}, func(out string, err error) tea.Msg { return taskOutputMsg{taskID: taskID, output: out, err: err} })
}

// cancelTask stops a background task. The endpoint and the client method
// both already existed; nothing in either client called them, so a task
// that would not finish had no exit short of restarting the daemon.
func (m Model) cancelTask(taskID string) tea.Cmd {
	return callErr(func(ctx context.Context) error {
		return m.client.CancelTask(ctx, taskID)
	}, func(err error) tea.Msg { return taskCancelledMsg{taskID: taskID, err: err} })
}

func (m Model) fetchSlashCommands() tea.Cmd {
	return call(m.client.ListSlashCommands, func(c []client.SlashCommandInfo, err error) tea.Msg {
		return slashCommandsMsg{commands: c, err: err}
	})
}

func (m Model) fetchSkills() tea.Cmd {
	return call(m.client.ListSkills, func(s []client.SkillInfo, err error) tea.Msg { return skillsMsg{skills: s, err: err} })
}

// fetchSessions lists the conversations this daemon holds, for the
// "/session" picker.
func (m Model) fetchSessions() tea.Cmd {
	return call(m.client.ListSessions, func(s []session.Session, err error) tea.Msg { return sessionsMsg{sessions: s, err: err} })
}

func (m Model) fetchCommands() tea.Cmd {
	return call(m.client.ListCommands, func(c []client.CommandInfo, err error) tea.Msg { return commandsMsg{commands: c, err: err} })
}

// switchAgent asks the daemon to change this session's active agent. It
// reports only errors back to Update — the actual state change (and the
// transcript line announcing it) comes from the agent.switched event this
// same call causes the daemon to broadcast, which every subscribed client
// (including this one) receives the same way.
func (m Model) switchAgent(name string) tea.Cmd {
	return callErr(func(ctx context.Context) error {
		_, err := m.client.SwitchAgent(ctx, m.sessionID, name)
		return err
	}, func(err error) tea.Msg { return opErrMsg{op: "switch agent", err: err} })
}
