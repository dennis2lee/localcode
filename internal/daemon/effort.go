package daemon

import (
	"encoding/json"
	"net/http"
)

// How hard the model is asked to think, over HTTP.
//
// A per-session setting like the four permission switches, and shaped the
// same way: a GET that says what is in force and what the choices are, a
// POST that changes it, and an event so a second client watching the same
// conversation redraws rather than showing yesterday's answer.
//
// The choices come with the answer rather than from a constant in each
// client, because they are a property of the model the conversation is
// on: Anthropic's newest families have one switch where muse has four
// steps, and a client with a hard-coded list offers steps that do
// nothing on three models out of four.

func (d *Daemon) handleGetSessionEffort(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := d.Loop.Store.Get(id); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, d.Loop.EffortView(id))
}

// handleSetSessionEffort sets the level for the model this conversation
// is on. An empty level clears it — this conversation's answer for that
// model, and the conversation-wide one set before this was per model,
// since that is what would otherwise still be in force — so the
// profile's answer applies again. A real third state, not a tidy-up: a
// conversation that has never been asked and one that was asked and said
// off look identical from outside, and only one of them should change
// when the profile does.
//
// Not refused while a turn is running. The level is fixed when a turn
// starts, like every other field of the request, so a change during one
// takes effect on the next.
func (d *Daemon) handleSetSessionEffort(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := d.Loop.Store.Get(id); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req struct {
		Level string `json:"level"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	// SetSessionEffort announces for itself, because the chat command
	// changes the level too and both have to reach the readouts.
	view, err := d.Loop.SetSessionEffort(id, req.Level)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// announceEffort tells every client watching this conversation. Written
// to the session log like permissions.changed, so a client that joins
// later replays it rather than having to ask.
//
// The Loop owns it, because "/effort" changes the level too and a route
// that announces beside a command that does not is a readout that is
// right half the time.
func (d *Daemon) announceEffort(sessionID string) {
	d.Loop.AnnounceEffort(sessionID)
}
