package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// What the desktop window remembers for the person in front of it: the
// faces and the text size, the zoom, how wide the panels were dragged,
// which panels a conversation shows, which groups are folded.
//
// The page keeps all of that in the browser's own storage, and in a
// browser that is the right place: it belongs to the person and their
// screen, not to the daemon. In the desktop window the same storage turned
// out not to last. It is kept per address, port included, and per
// program file name, and the window's port is only remembered, not owned.
// Each of these was measured in the real window on Windows to bring the
// text size back at its default after a restart, and to leave it there:
//
//   - another program on the port the window last used;
//   - the port inside a range the system reserved, which Hyper-V, WSL and
//     Docker do at boot with a different set each time;
//   - a second window open at the same time, which takes another port
//     and writes that one down as the last used;
//   - the program started under another file name.
//
// What did not lose anything, in the same measurements: closing the
// window at once after a change, and ending the process by force two
// seconds after one. The storage itself is sound. Its address is not.
//
// So the window's page writes each change here as well, and reads the
// whole set back before it draws (see static/js/kept.js and
// static/js/prefs.js). One small file beside the config, written before
// the request is answered. Not in config.json: that file is somebody's to
// write and to carry between machines, and this is a fact about one
// screen.
//
// Only the desktop window's page uses it. A browser tab keeps its own
// storage as before, on a port that does not move.

const (
	// One value. The largest the page writes is which panels each of the
	// last two hundred conversations shows, a few kilobytes.
	maxWindowStateValue = 256 << 10
	// The whole file, and the keys one request may name.
	maxWindowStateFile = 1 << 20
	maxWindowStateKeys = 64
)

// windowStateKey is a name the page may keep something under: its own
// prefix and nothing else, so this is never a place to put a file's worth
// of anything else.
var windowStateKey = regexp.MustCompile(`^(?:localcode\.[A-Za-z0-9][A-Za-z0-9._-]{0,63}|archiveOpen)$`)

// readWindowState reads the file. kept is false when there is nothing to
// read yet: no file, or one that is not what this writes, which is treated
// the same way so the page fills it in again from what it has.
func readWindowState(path string) (state map[string]string, kept bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return map[string]string{}, false
	}
	state = make(map[string]string, len(raw))
	for k, v := range raw {
		var s string
		if !windowStateKey.MatchString(k) || json.Unmarshal(v, &s) != nil || len(s) > maxWindowStateValue {
			continue
		}
		state[k] = s
	}
	return state, true
}

// handleGetWindowState answers with everything kept. "kept" is null until
// the page has written here once, which is how a page that already holds
// settings in its own storage knows to hand them over rather than have
// them replaced by nothing.
func (d *Daemon) handleGetWindowState(w http.ResponseWriter, r *http.Request) {
	if !d.sameSiteGuard(w, r, "the window's settings") {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if d.WindowStatePath == "" {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "kept": nil})
		return
	}
	d.windowStateMu.Lock()
	state, kept := readWindowState(d.WindowStatePath)
	d.windowStateMu.Unlock()
	if !kept {
		writeJSON(w, http.StatusOK, map[string]any{"available": true, "kept": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "kept": state})
}

// handleSetWindowState writes the named keys and leaves the rest: a string
// keeps it, null forgets it. The file is on disk when this answers.
func (d *Daemon) handleSetWindowState(w http.ResponseWriter, r *http.Request) {
	if !d.sameSiteGuard(w, r, "the window's settings") {
		return
	}
	if d.WindowStatePath == "" {
		writeError(w, http.StatusNotFound, errors.New("this daemon keeps no window settings"))
		return
	}
	var req map[string]*string
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWindowStateFile)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("the body must be an object of names to a string or null"))
		return
	}
	if len(req) > maxWindowStateKeys {
		writeError(w, http.StatusBadRequest, fmt.Errorf("at most %d names in one request", maxWindowStateKeys))
		return
	}
	for k, v := range req {
		if !windowStateKey.MatchString(k) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("%q is not a name the window keeps a setting under", k))
			return
		}
		if v != nil && len(*v) > maxWindowStateValue {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("the value of %q is larger than %d bytes", k, maxWindowStateValue))
			return
		}
	}

	d.windowStateMu.Lock()
	defer d.windowStateMu.Unlock()
	state, _ := readWindowState(d.WindowStatePath)
	for k, v := range req {
		if v == nil {
			delete(state, k)
		} else {
			state[k] = *v
		}
	}
	out, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(out) > maxWindowStateFile {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("the window's settings would be larger than %d bytes", maxWindowStateFile))
		return
	}
	if err := writeWindowState(d.WindowStatePath, append(out, '\n')); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("could not write %s: %w", d.WindowStatePath, err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeWindowState replaces the file in one step, so a crash or a full
// disk leaves the old one rather than half of the new one.
//
// The rename is tried a few times. On Windows it is refused for as long as
// another process has the file open, and two things do that for a moment:
// a second window reading it, and a virus scanner looking at what was just
// written.
func writeWindowState(path string, out []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".window-state-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // nothing to remove once the rename has happened
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		err = os.Rename(name, path)
		if err == nil || attempt == 4 {
			return err
		}
		time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
	}
}
