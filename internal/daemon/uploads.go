package daemon

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// maxUploadBytes bounds one uploaded file (drag-and-drop attachments,
// mainly) — generous enough for source files/screenshots without letting
// a client exhaust disk space.
const maxUploadBytes = 32 << 20 // 32MB

// handleUploadFile saves a drag-and-dropped file to
// ~/.localcode/uploads/<session-id>/<sanitized-filename> and returns its
// absolute path, so the caller can splice a reference to it into the next
// chat message (the model then reads it with its own file tools — there's
// no separate "attachment" concept in the wire protocol).
func (d *Daemon) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := d.Loop.Store.Get(id); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("parse upload: %w", err))
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf(`missing "file" form field: %w`, err))
		return
	}
	defer file.Close()

	// filepath.Base strips any directory components the client sent, so a
	// crafted filename like "../../etc/passwd" can't escape the uploads
	// dir; "." and ".." themselves are rejected outright.
	name := filepath.Base(header.Filename)
	if name == "" || name == "." || name == ".." {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid filename %q", header.Filename))
		return
	}

	home, err := os.UserHomeDir()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("resolve home dir: %w", err))
		return
	}
	dir := filepath.Join(home, ".localcode", "uploads", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("create uploads dir: %w", err))
		return
	}

	path := filepath.Join(dir, name)
	dst, err := os.Create(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("create %s: %w", path, err))
		return
	}
	defer dst.Close()
	if _, err := io.Copy(dst, io.LimitReader(file, maxUploadBytes)); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("write %s: %w", path, err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"path": path})
}
