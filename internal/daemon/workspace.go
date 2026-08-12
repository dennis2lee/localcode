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

// handleGetWorkspace reports the directory a session's file paths and bash
// commands resolve against, and whether this daemon can open a native
// folder picker for it.
//
// ?session=<id> asks about one session, which is what a client should do:
// the workspace is per-session now, so a daemon-wide answer is only the
// default a session inherits when it has none of its own.
func (d *Daemon) handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"path":       d.Loop.SessionDir(r.URL.Query().Get("session")),
		"can_browse": d.PickDirectory != nil,
		"can_reveal": d.RevealDirectory != nil,
	})
}

// handleRevealWorkspace opens the current workspace in the machine's file
// manager — the folder icon beside the workspace name in the header.
//
// The daemon's own working directory, not a path from the request: this
// starts a process with a path argument, and taking that path from the
// caller would make it a way to ask the daemon to open anything at all.
// There is nothing to choose here anyway — the button means "show me this
// folder", and which folder that is, is not the client's to say.
func (d *Daemon) handleRevealWorkspace(w http.ResponseWriter, r *http.Request) {
	if d.RevealDirectory == nil {
		http.Error(w, "this daemon cannot open a file-manager window (it is only available in the desktop-window mode)", http.StatusNotFound)
		return
	}
	dir := d.Loop.SessionDir(r.URL.Query().Get("session"))
	if err := d.RevealDirectory(r.Context(), dir); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": dir})
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

// handleSetWorkspace moves a session into a different directory.
//
// Per-session, as of v0.39.0. It used to be process-wide — os.Chdir plus
// Loop.ProjectDir — and everything awkward about it followed from that:
// the change had to be refused while a turn was running in *any* session,
// including one nobody was watching and one parked forever on an
// unanswered permission request, and two clients on one daemon could not
// work in two projects because moving one moved the other. Now each turn
// carries its session's directory on the context (see agent.SessionDir and
// tools.WithWorkingDir), so a move touches one session and nothing else.
//
// Still refused while *this* session has a turn in flight: its own tool
// call, mid-execution, would otherwise find the ground moved under it.
// That is a real race rather than a shared-state artifact, and it is one
// the person asking can see and wait out, because it is the session in
// front of them.
func (d *Daemon) handleSetWorkspace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		// SessionID is the session being moved. Omitted, the directory
		// becomes the daemon's default: what a newly created session
		// starts in, and what a session with no recorded workspace of its
		// own falls back to.
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil || req.Path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}

	abs, err := filepath.Abs(req.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("resolve path: %w", err))
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("stat %s: %w", abs, err))
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%s is not a directory", abs))
		return
	}

	// No session named: this is the default for sessions that have none.
	if req.SessionID == "" {
		d.Loop.SetProjectDir(abs)
		writeJSON(w, http.StatusOK, map[string]any{"path": abs})
		return
	}

	// The check and the move together, not one after the other: a turn
	// starting in the gap would run its first relative-path write against
	// whichever directory won the race.
	busy, err := d.turns.whileSessionIdle(req.SessionID, func() error {
		if _, err := d.Loop.Store.SetWorkspace(req.SessionID, abs); err != nil {
			return &httpError{http.StatusNotFound, err.Error()}
		}
		return nil
	})
	if busy {
		// One session now, and it is the one the person is looking at, so
		// the id is enough to act on. Still sent as data: the client
		// offers to stop it and try again rather than reprinting a
		// sentence about it.
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "a turn is in progress in this session; stop it or wait for it before switching workspace",
			"busy":  []string{req.SessionID},
		})
		return
	}
	if err != nil {
		writeHTTPError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"path": abs})
}
