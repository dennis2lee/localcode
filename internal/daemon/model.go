package daemon

import (
	"encoding/json"
	"net/http"
)

// Which model a conversation answers on, over HTTP.
//
// Shaped exactly like the effort endpoints beside it: a GET that says
// what is in force and what the choices are, a POST that changes it, and
// an event so a second client watching the same conversation redraws.
//
// The choices come with the answer rather than from a constant in each
// client, because they are a property of this config — which profiles it
// declares, and which provider serves each — and no client can know that
// without asking.

func (d *Daemon) handleGetSessionModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := d.Loop.Store.Get(id); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, d.Loop.ModelView(id))
}

// handleSetSessionModel points this conversation at one of the config's
// profiles, keeping the agent it is talking to. An empty profile clears
// the choice so the agent's own applies again.
//
// Not refused while a turn is running, for the reason the effort one is
// not: the profile is resolved when a turn starts, like every other
// field of the request, so a change during one takes effect on the next.
func (d *Daemon) handleSetSessionModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := d.Loop.Store.Get(id); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	var req struct {
		Profile string `json:"profile"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	// SetSessionModel announces for itself, because "/model" changes it
	// too and both have to reach the readouts.
	view, err := d.Loop.SetSessionModel(id, req.Profile)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
