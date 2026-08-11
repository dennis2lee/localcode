package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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
	_ = json.NewDecoder(jsonBody(w, r)).Decode(&req)

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
		// SessionID is the session the workspace is being changed for. It
		// is optional (a client that doesn't track sessions can omit it),
		// but without it the move is invisible to the session list, and
		// re-selecting the session later would put the workspace back
		// where the session was created — which is what made a switch
		// look like it had not taken.
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil || req.Path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}

	// Named, because the working directory is one process-wide thing and
	// this guard is therefore daemon-wide: the blocking turn is often in
	// a session the user is not looking at. A turn stuck on an unanswered
	// permission request blocks every workspace change until it is
	// answered, and "a turn is in progress" gave no way to find it.
	// The whole check-and-move runs with turns held off, not just the
	// check. A turn that began in the gap ran its first relative-path
	// write against the new directory — a file written into the wrong
	// repository, with nothing reported anywhere.
	var abs string
	busy, err := d.turns.whileIdle(func() error {
		var err error
		abs, err = filepath.Abs(req.Path)
		if err != nil {
			return &httpError{http.StatusBadRequest, fmt.Sprintf("resolve path: %v", err)}
		}
		info, err := os.Stat(abs)
		if err != nil {
			return &httpError{http.StatusBadRequest, fmt.Sprintf("stat %s: %v", abs, err)}
		}
		if !info.IsDir() {
			return &httpError{http.StatusBadRequest, fmt.Sprintf("%s is not a directory", abs)}
		}
		if err := os.Chdir(abs); err != nil {
			return &httpError{http.StatusInternalServerError, fmt.Sprintf("chdir %s: %v", abs, err)}
		}
		return nil
	})
	if len(busy) > 0 {
		// Named, because the working directory is one process-wide thing
		// and this guard is therefore daemon-wide: the blocking turn is
		// often in a session the user is not looking at. A turn stuck on
		// an unanswered permission request blocks every workspace change
		// until it is answered, and "a turn is in progress" gave no way
		// to find it.
		http.Error(w, fmt.Sprintf(
			"a turn is in progress in %s; cancel or wait for it before switching workspace (a session waiting on a permission request stays busy until you answer it)",
			strings.Join(busy, ", ")), http.StatusConflict)
		return
	}
	if err != nil {
		writeHTTPError(w, err)
		return
	}

	d.Loop.SetProjectDir(abs)

	// Record the move on the session that asked for it. Non-fatal on
	// failure: the workspace has already changed and reporting that
	// truthfully matters more than the bookkeeping — an unknown session id
	// (a client that made one up, or a session deleted mid-request) must
	// not turn a successful switch into an error.
	if req.SessionID != "" {
		if _, err := d.Loop.Store.SetWorkspace(req.SessionID, abs); err != nil {
			log.Printf("workspace: could not record %s on session %s: %v", abs, req.SessionID, err)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"path": abs})
}
