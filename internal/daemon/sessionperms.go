package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"

	"localcode/internal/events"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// The four permission switches, per session.
//
// Per session because the workspace is: two conversations on one daemon
// are two projects, and every one of these four is a sentence about a
// project. "Do not ask me about this one" said in a scratch experiment
// used to silence the prompts in the conversation editing something that
// mattered, which is the same class of bug as a background task working
// in the wrong directory.
//
// config.json still holds the defaults, and a session that has not
// answered a question for itself follows them. That is what makes the
// file worth writing: it is the answer for every conversation that has
// not disagreed.

// permissionsView is what a client needs to draw the panel: the answer,
// where it came from, and what has been remembered.
func (d *Daemon) permissionsView(sessionID string) map[string]any {
	effective := map[string]bool{}
	source := map[string]string{}
	for _, sw := range session.Switches() {
		on, from := d.Policy.Effective(sessionID, sw)
		effective[string(sw)] = on
		source[string(sw)] = string(from)
	}
	return map[string]any{
		"session_id": sessionID,
		"effective":  effective,
		// Where each answer comes from, so a checkbox that is on can say
		// whether this conversation turned it on, inherited it from the
		// conversation that spawned it, or is following config.json. A
		// switch that looks the same in every session but means three
		// different things is a switch nobody trusts.
		"source": source,
		"remembered": map[string][]string{
			"read":  emptyIfNil(d.Broker.RememberedOutside(sessionID, tools.OutsideRead)),
			"write": emptyIfNil(d.Broker.RememberedOutside(sessionID, tools.OutsideWrite)),
		},
	}
}

// emptyIfNil keeps the JSON an array rather than null, so a client can
// read .length without checking.
func emptyIfNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// announceSessionPermissions tells anyone watching this conversation that
// its switches moved. Appended to the session's own log rather than
// broadcast daemon-wide, because that is the scope of the fact.
func (d *Daemon) announceSessionPermissions(sessionID string) {
	d.Loop.Store.Append(sessionID, events.TypePermissionsChanged, d.permissionsView(sessionID))
}

func (d *Daemon) handleGetSessionPermissions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := d.Loop.Store.Get(id); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, d.permissionsView(id))
}

// handleSetSessionPermission sets one switch for one session.
//
// enabled is a pointer, and null clears the session's own answer so the
// daemon default applies again. That is a real third state and not a
// tidy-up: a session that has never been asked and one that was asked and
// said no look identical from the outside, and only one of them should
// change when the default does.
func (d *Daemon) handleSetSessionPermission(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Switch  string `json:"switch"`
		Enabled *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	sw, ok := knownSwitch(req.Switch)
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown permission switch %q", req.Switch))
		return
	}
	if err := d.Policy.Set(id, sw, req.Enabled); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	d.announceSessionPermissions(id)
	writeJSON(w, http.StatusOK, d.permissionsView(id))
}

// handleForgetSessionOutside is "/read-outside mem-clear" over HTTP:
// drop the directories this session approved leaving the workspace for.
func (d *Daemon) handleForgetSessionOutside(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Class string `json:"class"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	class, ok := knownClass(req.Class)
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown class %q (want \"read\" or \"write\")", req.Class))
		return
	}
	if _, err := d.Loop.Store.Get(id); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	d.Broker.ForgetOutside(id, class)
	d.announceSessionPermissions(id)
	writeJSON(w, http.StatusOK, d.permissionsView(id))
}

func knownSwitch(name string) (session.Switch, bool) {
	for _, sw := range session.Switches() {
		if string(sw) == name {
			return sw, true
		}
	}
	return "", false
}

func knownClass(name string) (tools.OutsideClass, bool) {
	switch name {
	case "read":
		return tools.OutsideRead, true
	case "write":
		return tools.OutsideWrite, true
	}
	return tools.OutsideNone, false
}
