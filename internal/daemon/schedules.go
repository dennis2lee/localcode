package daemon

import (
	"errors"
	"net/http"

	"localcode/internal/agent"
)

// Work booked for later, over HTTP.
//
// The list is read once when a conversation is opened; after that a
// client follows the schedule.* events on that conversation's own stream,
// the same way the background-task rows work. There is no separate
// broadcast, because a schedule belongs to a conversation and not to the
// daemon.

func (d *Daemon) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := d.Loop.Store.Get(id); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	list := []agent.Scheduled{}
	if d.Loop.Schedules != nil {
		list = append(list, d.Loop.Schedules.List(id)...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": list})
}

// handleSeenSchedule records that somebody opened the result. It is the
// third state of the row's light: blinking while it waits, solid once
// there is something to read, grey once it has been read.
func (d *Daemon) handleSeenSchedule(w http.ResponseWriter, r *http.Request) {
	if d.Loop.Schedules == nil {
		writeError(w, http.StatusNotFound, errNoScheduler)
		return
	}
	d.Loop.Schedules.MarkSeen(r.PathValue("sid"))
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteSchedule removes a booked task, and the session its run
// left behind if there is one.
//
// Both, because the row and the transcript are the same thing to the
// person clicking Delete: leaving the run session behind would leave a
// conversation nothing lists and nothing can reach, which is how the
// background tasks accumulated orphans before their own delete went
// through the ordinary session delete.
func (d *Daemon) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	if d.Loop.Schedules == nil {
		writeError(w, http.StatusNotFound, errNoScheduler)
		return
	}
	sid := r.PathValue("sid")
	entry, found := d.Loop.Schedules.Get(sid)
	if !d.Loop.Schedules.Cancel(sid) {
		writeError(w, http.StatusNotFound, errNoSuchSchedule)
		return
	}
	if found && entry.RunSession != "" {
		// Best effort: a run session that is already gone is not an error
		// worth failing the delete over, and the row is removed either way.
		d.Loop.ClearSessionState(entry.RunSession)
		d.Broker.ForgetSession(entry.RunSession)
		_ = d.Loop.Store.Delete(entry.RunSession)
	}
	w.WriteHeader(http.StatusNoContent)
}

var (
	errNoScheduler    = errors.New("this daemon has no scheduler")
	errNoSuchSchedule = errors.New("no such scheduled task")
)
