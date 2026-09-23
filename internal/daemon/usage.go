package daemon

import (
	"errors"
	"net/http"
	"time"
)

// handleUsage is "/usage all|today|week|month" as data, for the Web UI's
// usage window: the same conversations, read from the same logs, so the
// window and the command cannot disagree. window defaults to all.
//
// The window used to add the totals up itself, one event stream per
// listed conversation, and a list shows neither the sessions of
// sub-agents, scheduled runs and debate reviewers nor where a fork's copy
// of another log ends: the window and the command beside it gave two
// different totals for the same daemon.
func (d *Daemon) handleUsage(w http.ResponseWriter, r *http.Request) {
	word := r.URL.Query().Get("window")
	if word == "" {
		word = "all"
	}
	summary, ok := d.Loop.UsageAcross(word, time.Now())
	if !ok {
		writeError(w, http.StatusBadRequest, errors.New("window must be all, today, week or month"))
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// handleSessionUsage is what one conversation and every session below it
// spent, which is what "localcode run --format json" reports as a run's
// usage when the run went through a daemon: a run's sub-agents make their
// calls in sessions of their own.
func (d *Daemon) handleSessionUsage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := d.Loop.Store.Get(id); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, d.Loop.UsageOfTree(id))
}
