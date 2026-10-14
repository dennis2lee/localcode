package daemon

import (
	"errors"
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

// uploadFraming is the multipart framing allowed around one file: its
// boundaries, its part headers and the form field name.
const uploadFraming = 64 << 10

// handleUploadFile saves a drag-and-dropped file to
// ~/.localcode/uploads/<session-id>/<sanitized-filename> and returns its
// absolute path, so the caller can splice a reference to it into the next
// chat message (the model then reads it with its own file tools — there's
// no separate "attachment" concept in the wire protocol).
func (d *Daemon) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := d.Loop.Store.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if refuseArchived(w, sess) {
		return
	}

	// The body is bounded as well as the file: ParseMultipartForm keeps
	// what does not fit in memory in temporary files, so an unbounded body
	// was an unbounded write to the disk before any check below ran. The
	// slack is the multipart framing around one file.
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+uploadFraming)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("upload is over the %d MB limit", maxUploadBytes>>20))
			return
		}
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
	// One byte past the limit is read so a file over it can be told apart
	// from one exactly at it. A larger file used to be cut at the limit and
	// answered 200, so the model read a truncated file as the whole of it.
	n, err := io.Copy(dst, io.LimitReader(file, maxUploadBytes+1))
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("write %s: %w", path, err))
		return
	}
	if n > maxUploadBytes {
		dst.Close()
		os.Remove(path)
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("%s is over the %d MB upload limit", name, maxUploadBytes>>20))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"path": path})
}
