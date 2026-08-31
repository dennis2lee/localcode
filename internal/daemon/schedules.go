package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"localcode/internal/agent"
	"localcode/internal/when"
	"strings"
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
	d.Loop.Schedules.MarkSeen(r.PathValue("id"), r.PathValue("sid"))
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
	id, sid := r.PathValue("id"), r.PathValue("sid")
	entry, found := d.Loop.Schedules.Get(id, sid)
	if !d.Loop.Schedules.Cancel(id, sid) {
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

// handleBookSchedule is the window's Schedule button: the moment and the
// request arrive as two fields.
//
// Two fields rather than one string is the whole difference from
// "/schedule". At a prompt the split between the two has to be guessed —
// where does "내일 아침에" end and the request begin — and here there is
// nothing to guess. The time is still parsed by the same parser, so the
// two ways of booking cannot disagree about what "내일 아침" means.
func (d *Daemon) handleBookSchedule(w http.ResponseWriter, r *http.Request) {
	if d.Loop.Schedules == nil {
		writeError(w, http.StatusNotFound, errNoScheduler)
		return
	}
	id := r.PathValue("id")
	sess, err := d.Loop.Store.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if refuseArchived(w, sess) {
		return
	}
	var req struct {
		When   string `json:"when"`
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	now := time.Now()
	at, err := when.ParseTime(req.When, now)
	if err != nil {
		// 400 with the parser's own sentence: it names which kind of no
		// this is, which is the half a status code cannot carry.
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeError(w, http.StatusBadRequest, errors.New("say what to do at that time"))
		return
	}
	agentName := sess.Agent
	if agentName == "" {
		agentName = "general-purpose"
	}
	entry, err := d.Loop.Schedules.Add(id, agentName, strings.TrimSpace(req.Prompt), at)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"schedule":  entry,
		"human":     when.Format(at, now),
		"workspace": d.Loop.SessionDir(id),
	})
}

// handlePreviewSchedule answers "what would that time mean", without
// booking anything.
//
// It exists so the window can show the answer while somebody is still
// typing it. That echo is the whole reason the parser is allowed to guess
// at all: a misread moment is caught before the work is booked rather
// than by the work not happening. Answered by the daemon rather than by a
// second parser in the page, because two parsers that disagree about
// "내일 아침" is the bug this is meant to prevent.
func (d *Daemon) handlePreviewSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		When string `json:"when"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	now := time.Now()
	at, err := when.ParseTime(req.When, now)
	if err != nil {
		// 200 with the reason: this is a preview of something being
		// typed, and a red status code for a half-typed word is noise.
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"at":    at,
		"human": when.Format(at, now),
	})
}

// handleRenameSchedule sets a booked task's name, the panel's own label
// for it. Cosmetic, like a session's title: nothing resolves by it.
func (d *Daemon) handleRenameSchedule(w http.ResponseWriter, r *http.Request) {
	if d.Loop.Schedules == nil {
		writeError(w, http.StatusNotFound, errNoScheduler)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	entry, ok := d.Loop.Schedules.Rename(r.PathValue("id"), r.PathValue("sid"), req.Name)
	if !ok {
		writeError(w, http.StatusNotFound, errNoSuchSchedule)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}
