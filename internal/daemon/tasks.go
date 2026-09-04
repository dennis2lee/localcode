package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"localcode/internal/events"
)

func (d *Daemon) handleResolvePermission(w http.ResponseWriter, r *http.Request) {
	permID := r.PathValue("permId")
	var req struct {
		Allow bool `json:"allow"`
		// Scope is one of "once" (default), "session", or "always". See
		// agent.PermissionBroker.Resolve.
		Scope string `json:"scope"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !d.Broker.Resolve(permID, req.Allow, req.Scope) {
		// Nothing was waiting on this id. Said plainly rather than
		// answered with "resolved": the usual cause is a request that was
		// already answered — from another client, or from a replay of the
		// session log — and a client told it succeeded will go on
		// believing the turn it is watching has been unblocked.
		writeError(w, http.StatusNotFound, fmt.Errorf(
			"permission request %s is not pending (already answered, or from a turn that has since ended)", permID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

func (d *Daemon) handleSpawnTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// A task started in a conversation that has been put away is work
	// nobody will come back for. The store refuses it too, under its own
	// mutex, which is the check that cannot be raced; this is the one that
	// produces a message worth reading.
	if sess, err := d.Loop.Store.Get(id); err == nil && refuseArchived(w, sess) {
		return
	}
	var req struct {
		Agent  string `json:"agent"`
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Agent == "" || req.Prompt == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("agent and prompt are required"))
		return
	}

	taskID, err := d.Tasks.Spawn(id, req.Agent, req.Prompt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"task_id": taskID})
}

func (d *Daemon) handleListTasks(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	writeJSON(w, http.StatusOK, d.Tasks.List(id))
}

// handleTaskOutput returns everything a background task's model has said
// so far — a task is a session, so this reads its event log and works
// mid-run, which is what makes "/tasks <id>" useful as a progress view
// rather than only a post-mortem.
func (d *Daemon) handleTaskOutput(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskId")
	// It has to actually be a task. The id went straight to the event
	// log, so this returned any conversation in the daemon while calling
	// it a task — a background task is a child session, and a session
	// with no parent is somebody's conversation.
	if sess, err := d.Loop.Store.Get(taskID); err != nil || sess.ParentID == "" {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown task %s", taskID))
		return
	}
	evs, err := d.Loop.Store.Events(taskID, 0)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown task %s", taskID))
		return
	}
	var out strings.Builder
	for _, ev := range evs {
		switch ev.Type {
		case events.TypeMessagePartDelta:
			if text, ok := ev.Data["text"].(string); ok {
				out.WriteString(text)
			}
		case events.TypeMessagePartEnd:
			out.WriteString("\n")
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": strings.TrimRight(out.String(), "\n")})
}

func (d *Daemon) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskId")
	ok := d.Tasks.Cancel(taskID)
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": ok})
}

// handleResolveInput answers a mid-turn question from the model.
//
// Its own route rather than a mode on the permission one, for the same
// reason the broker is its own type: the payload is an answer, not an
// allow with a scope, and one endpoint taking both would have to decide
// which it was looking at.
func (d *Daemon) handleResolveInput(w http.ResponseWriter, r *http.Request) {
	askID := r.PathValue("askId")
	var req struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Answer) == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("answer is empty"))
		return
	}
	if d.Loop == nil || d.Loop.Input == nil || !d.Loop.Input.Resolve(askID, req.Answer) {
		// Same reasoning as the permission route: a client told this
		// succeeded would believe the turn it is watching is unblocked.
		writeError(w, http.StatusNotFound, fmt.Errorf(
			"question %s is not pending (already answered, or from a turn that has since ended)", askID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}
