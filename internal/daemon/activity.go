package daemon

import (
	"localcode/internal/events"
)

// What a session is doing right now, as the session list's light shows it.
//
// Four facts, and a session can hold several at once: a turn in flight, a
// background task it started that has not finished, a permission request
// waiting for a person, and a question the model asked a person. The list
// used to hear only about the first. A conversation whose turn had ended
// with a task still working drew as finished, and one stopped on a
// question in another session drew as working until the list was loaded
// again.

// sessionActivity is one session's state, as the session list and the
// session.activity event report it.
type sessionActivity struct {
	// Busy is a turn in flight here, or in the daemon this one replaced.
	Busy bool
	// Tasks counts the tasks running for the session, at any depth.
	Tasks int
	// Asking is a question waiting for a person: a permission request or
	// the model's own question.
	Asking bool
}

func (d *Daemon) activityOf(id string) sessionActivity {
	a := sessionActivity{
		Busy:  len(d.turns.anyBusy([]string{id})) > 0 || d.ownedElsewhereFor(id),
		Tasks: d.Tasks.RunningFor(id),
	}
	a.Asking = d.Broker.Asking()[id]
	if d.Loop != nil && d.Loop.Input.Asking()[id] {
		a.Asking = true
	}
	return a
}

// announceActivity tells every client what session id is doing now: the
// whole state, not the one fact that moved, so a client can replace what
// it holds rather than merge into it. Computed and sent under one lock,
// so two changes racing each other reach clients in the order they were
// read and the last one a client sees is the latest state.
func (d *Daemon) announceActivity(id string) {
	d.activityMu.Lock()
	defer d.activityMu.Unlock()
	a := d.activityOf(id)
	d.daemonEvents.send(events.Event{
		Type: events.TypeSessionActivity,
		Data: map[string]any{"session": id, "busy": a.Busy, "tasks": a.Tasks, "asking": a.Asking},
	})
}

// ownedElsewhereFor reports whether the daemon this one replaced is still
// working for session id: in it, or in a task or a scheduled run under it.
// Its manifest names the sessions it is writing, which for a task is the
// task's own, and the conversation the task works for drew as idle here
// while the old daemon finished it.
func (d *Daemon) ownedElsewhereFor(id string) bool {
	for owned := range d.sessionsOwnedElsewhere() {
		if owned == id || (d.Loop != nil && d.Loop.TopLevelSession(owned) == id) {
			return true
		}
	}
	return false
}
