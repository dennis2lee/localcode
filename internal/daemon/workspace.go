package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"localcode/internal/dialog"
)

// handleGetWorkspace reports the daemon's current working directory — the
// root every relative file path and bash command resolves against — and
// whether this daemon can open a native folder picker for it.
func (d *Daemon) handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"path":       d.Loop.GetProjectDir(),
		"can_browse": d.PickDirectory != nil,
	})
}

// handleBrowseWorkspace opens the OS folder picker and returns the chosen
// directory, without applying it — the caller still POSTs it to
// /api/workspace, so picking and switching stay separately reportable (a
// switch can be refused mid-turn long after the dialog closed).
//
// A cancelled dialog is 204, not an error: dismissing a picker is a normal
// thing to do and shouldn't surface as a failure.
func (d *Daemon) handleBrowseWorkspace(w http.ResponseWriter, r *http.Request) {
	if d.PickDirectory == nil {
		http.Error(w, "this daemon has no native folder picker (it is only available in the desktop-window mode)", http.StatusNotFound)
		return
	}

	var req struct {
		Start string `json:"start"`
	}
	// A missing or unreadable body just means "no starting directory".
	_ = json.NewDecoder(r.Body).Decode(&req)

	// Tied to the request context so closing the tab or window takes the
	// dialog down with it, rather than leaving an orphaned helper process
	// waiting for an answer nobody is going to give.
	path, err := d.PickDirectory(r.Context(), req.Start)
	if errors.Is(err, dialog.ErrCancelled) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path})
}

// handleSetWorkspace changes the daemon's working directory for the rest of
// this run: os.Chdir (which every tool's relative-path resolution follows,
// since none of them carry their own base directory) plus updating
// Loop.ProjectDir (which only custom-command expansion reads directly).
//
// This is process-wide, not per-session — there is one workspace at a
// time, same as opening a different folder in an editor. Refused while any
// session has a turn in flight, since a tool call that's mid-execution
// against the old directory would otherwise silently start seeing the new
// one partway through.
func (d *Daemon) handleSetWorkspace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}

	if d.turns.anyRunning() {
		http.Error(w, "a turn is in progress; cancel or wait for it before switching workspace", http.StatusConflict)
		return
	}

	abs, err := filepath.Abs(req.Path)
	if err != nil {
		http.Error(w, fmt.Sprintf("resolve path: %v", err), http.StatusBadRequest)
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		http.Error(w, fmt.Sprintf("stat %s: %v", abs, err), http.StatusBadRequest)
		return
	}
	if !info.IsDir() {
		http.Error(w, fmt.Sprintf("%s is not a directory", abs), http.StatusBadRequest)
		return
	}
	if err := os.Chdir(abs); err != nil {
		http.Error(w, fmt.Sprintf("chdir %s: %v", abs, err), http.StatusInternalServerError)
		return
	}
	d.Loop.SetProjectDir(abs)
	writeJSON(w, http.StatusOK, map[string]any{"path": abs})
}
