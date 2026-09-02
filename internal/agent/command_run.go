package agent

import (
	"context"
)

// Running a command the model asked for.
//
// It runs as a turn of its own, in this same conversation, immediately
// after the turn that asked. Not inside that turn, for the reason a
// debate defers: a command drives turns in the session it belongs to, and
// the tool call that asked for it is inside one of them. Two writers on
// one history is what pendingDebate exists to avoid, and this is the same
// shape reached for the same reason.
//
// What runs is the line itself — "/compact", "/tidy-context foo" — fed
// back through SendMessage. So a built-in, a custom command and a skill
// all work with no case for any of them: the router already knows how to
// tell them apart, and it is the same router that answers the person.

type commandRunKey struct{}

// withCommandRun marks every turn a booked command produces, so the tool
// can refuse to book another from inside one.
func withCommandRun(ctx context.Context) context.Context {
	return context.WithValue(ctx, commandRunKey{}, true)
}

func inCommandRun(ctx context.Context) bool {
	v, _ := ctx.Value(commandRunKey{}).(bool)
	return v
}

// setPendingCommand records the command to run once this turn is over.
// One per session: a second call in the same turn replaces the first,
// because two commands queued behind one message is not a thing anybody
// meant, and the model has just been told to end its turn.
func (l *Loop) setPendingCommand(sessionID, line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pendingCommand == nil {
		l.pendingCommand = map[string]string{}
	}
	l.pendingCommand[sessionID] = line
}

// takePendingCommand removes and returns a session's booked command.
//
// Taken rather than read, and taken on every path out of a turn including
// the failing ones: a booking left behind would fire on some later and
// unrelated message.
func (l *Loop) takePendingCommand(sessionID string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	line, ok := l.pendingCommand[sessionID]
	delete(l.pendingCommand, sessionID)
	return line, ok
}

// ModelInvocableEnabled reports whether the model may run commands, and
// SetModelInvocableEnabled changes it. Live, like Smart Agent.
func (l *Loop) ModelInvocableEnabled() bool     { return l.Config.ModelInvocableLive() }
func (l *Loop) SetModelInvocableEnabled(v bool) { l.Config.SetModelInvocableRuntime(v) }

// ModelCommandNames is what the switch would let through if it were on:
// every command this session has an opt-in for, with slashes.
//
// Reported beside the switch rather than only inside the tool, so a
// settings window can say what turning it on reaches. A switch whose
// effect is invisible until a model happens to use it is one people turn
// on without knowing what they turned on.
func (l *Loop) ModelCommandNames() []string { return l.modelCommands() }
