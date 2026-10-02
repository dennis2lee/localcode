package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"localcode/internal/session"
)

// handleGetGroups returns the ordered list of group names. The order is
// the order they are drawn in, so the list is the answer on its own.
func (d *Daemon) handleGetGroups(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"names": d.groupNames()})
}

// handleSetGroups replaces the ordered list of group names wholesale, the
// way /api/sessions/order replaces the order. Creating, deleting and
// reordering are all just a different list; renaming is the one change
// that has to say so, because it carries the group's sessions with it.
func (d *Daemon) handleSetGroups(w http.ResponseWriter, r *http.Request) {
	var req struct {
		// A pointer, so that "no groups" and "the field was not sent" are
		// two different requests. They decode to the same empty slice
		// otherwise, and the whole-list shape means a body that forgot the
		// field — a typo, a client sending {} — would read as "the list is
		// now empty" and take every group down with it.
		Names  *[]string            `json:"names"`
		Rename *session.GroupRename `json:"rename"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Names == nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("names is required: send the whole list of group names, or [] to remove every group"))
		return
	}
	if err := d.Loop.Store.SetGroups(*req.Names, req.Rename); err != nil {
		writeError(w, groupRefusalStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"names": d.groupNames()})
}

// groupRefusalStatus separates "you asked for something invalid" from "the
// disk would not take it". Answering a failed write with 400 tells the
// browser to correct a request that was never wrong, so it retries
// nothing and the person is told their group name is bad when the disk is
// full.
//
// The store says which it was. Guessing from the error's shape does not
// work: a full disk arrives as a *fs.PathError and a marshalling failure
// arrives as a plain error, and both are the machine's fault.
func groupRefusalStatus(err error) int {
	if errors.Is(err, session.ErrPersist) {
		return http.StatusInternalServerError
	}
	return http.StatusBadRequest
}

// groupNames answers with an empty array rather than null, so the browser
// can hold the reply without checking which of the two it got.
func (d *Daemon) groupNames() []string {
	names := d.Loop.Store.GetGroups()
	if names == nil {
		return []string{}
	}
	return names
}

// handleSetSessionGroup puts one session in a group by name, or takes it
// out of every group when the name is empty. The group has to be one that
// exists: a session may not name a group into being, because the list is
// what decides the order groups are drawn in and a name arriving this way
// would have no place in it.
func (d *Daemon) handleSetSessionGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Group string `json:"group"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sess, err := d.Loop.Store.SetSessionGroup(id, req.Group)
	if err != nil {
		if errors.Is(err, session.ErrSessionNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, groupRefusalStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}
