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
