package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"localcode/internal/agent"
	"localcode/internal/browser"
	"localcode/internal/config"
)

// The browser panel's side of the daemon.
//
// The panel exists only in a GUI window, and the frame stream it opens is
// also how the daemon knows a GUI window is showing the conversation: the
// browser tool is offered to a conversation only while that stream is
// open (Loop.BrowserViewing). One connection says both things, so the
// two cannot disagree.

// refuseTheModelsBrowser turns away every request the model's browser
// makes (browser.UserAgentMark is in its User-Agent), on every route.
//
// The browser asks nothing before it acts, and it runs on the machine the
// daemon listens on. Without this, a page the model opened, or the model
// told by one, could open this API at whatever port it is on and turn
// every permission prompt off, add allow rules, or drive another
// conversation's tab; and could read every conversation through the Web
// UI the daemon serves.
func refuseTheModelsBrowser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.UserAgent(), browser.UserAgentMark) {
			http.Error(w, "localcode does not answer its own browser", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// browserViewers counts the open frame streams per conversation.
type browserViewers struct {
	mu sync.Mutex
	n  map[string]int
}

func (v *browserViewers) add(id string, delta int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.n == nil {
		v.n = map[string]int{}
	}
	v.n[id] += delta
	if v.n[id] <= 0 {
		delete(v.n, id)
	}
}

func (v *browserViewers) watching(id string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.n[id] > 0
}

// BrowserViewing reports whether a GUI window is showing sessionID's
// browser panel. Loop.BrowserViewing is set to this.
func (d *Daemon) BrowserViewing(sessionID string) bool {
	return d.Browser != nil && d.browserViewers.watching(sessionID)
}

// browserFor answers the requests below with the conversation's browser,
// or writes why there is none.
func (d *Daemon) browserFor(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if d.Browser == nil {
		writeError(w, http.StatusNotFound, errors.New("this daemon has no browser"))
		return "", false
	}
	if _, err := d.Loop.Store.Get(id); err != nil {
		writeError(w, http.StatusNotFound, err)
		return "", false
	}
	return id, true
}

// browserOn is browserFor for a route that starts or drives the browser:
// with the setting off it does neither, as the model's tool does not.
func (d *Daemon) browserOn(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, ok := d.browserFor(w, r)
	if !ok {
		return "", false
	}
	if !d.Loop.Config.BrowserLive() {
		writeError(w, http.StatusConflict, errors.New("the browser is off; turn it on in the settings window or with /browser on"))
		return "", false
	}
	return id, true
}

// handleBrowserStream serves the conversation's tab as a stream of JPEG
// frames (multipart/x-mixed-replace, which an <img> shows as it arrives).
// It waits while there is no tab yet, so the panel can open it first and
// see the page the moment the model or the person opens one.
func (d *Daemon) handleBrowserStream(w http.ResponseWriter, r *http.Request) {
	id, ok := d.browserOn(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}
	d.browserViewers.add(id, 1)
	defer d.browserViewers.add(id, -1)

	const boundary = "lcframe"
	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+boundary)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	write := func(frame []byte) {
		fmt.Fprintf(w, "--%s\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", boundary, len(frame))
		_, _ = w.Write(frame)
		_, _ = w.Write([]byte("\r\n"))
		flusher.Flush()
	}
	for ctx.Err() == nil {
		if d.Browser.Existing(id) == nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(300 * time.Millisecond):
			}
			continue
		}
		// Returns when the tab closes or the browser goes away; the loop
		// then waits for the next. Never straight back: a tab on a browser
		// that just died answers at once with an error, and a loop with no
		// pause spun on it.
		_ = d.Browser.Watch(ctx, id, write)
		select {
		case <-ctx.Done():
			return
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// handleBrowserState reports where the conversation's tab is.
func (d *Daemon) handleBrowserState(w http.ResponseWriter, r *http.Request) {
	id, ok := d.browserFor(w, r)
	if !ok {
		return
	}
	out := map[string]any{"has_tab": false, "width": browser.ViewportWidth, "height": browser.ViewportHeight}
	if tab := d.Browser.Existing(id); tab != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		out["has_tab"] = true
		if loc, err := tab.Location(ctx); err == nil {
			out["url"], out["title"] = loc.URL, loc.Title
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleBrowserNavigate is the panel's address bar and its back, forward
// and reload buttons. It opens the tab if the conversation has none, so
// the person can start the browser themselves.
func (d *Daemon) handleBrowserNavigate(w http.ResponseWriter, r *http.Request) {
	id, ok := d.browserOn(w, r)
	if !ok {
		return
	}
	var req struct {
		URL    string `json:"url"`
		Action string `json:"action"` // back, forward, reload; empty with url
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	tab, err := d.Browser.Tab(ctx, id)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	switch req.Action {
	case "":
		if req.URL == "" {
			// Opening the browser with nothing in it: the tab is enough.
			break
		}
		err = tab.Navigate(ctx, req.URL)
	case "back":
		err = tab.History(ctx, -1)
	case "forward":
		err = tab.History(ctx, 1)
	case "reload":
		err = tab.Reload(ctx)
	default:
		err = fmt.Errorf("unknown action %q", req.Action)
	}
	if errors.Is(err, browser.ErrBlocked) {
		writeError(w, http.StatusForbidden, errors.New(`a permission rule for "browser" denies that page, and it was not loaded`))
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	loc, _ := tab.Location(ctx)
	writeJSON(w, http.StatusOK, map[string]any{"url": loc.URL, "title": loc.Title})
}

// handleBrowserInput is the person clicking, scrolling and typing in the
// panel: one event per request, in the page's own pixels.
func (d *Daemon) handleBrowserInput(w http.ResponseWriter, r *http.Request) {
	id, ok := d.browserOn(w, r)
	if !ok {
		return
	}
	var req struct {
		Mouse *browser.MouseEvent `json:"mouse"`
		Key   *browser.KeyEvent   `json:"key"`
		Text  string              `json:"text"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	tab := d.Browser.Existing(id)
	if tab == nil {
		writeError(w, http.StatusConflict, errors.New("this conversation has no browser tab"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var err error
	switch {
	case req.Mouse != nil:
		err = tab.Mouse(ctx, *req.Mouse)
	case req.Key != nil:
		err = tab.KeyEvent(ctx, *req.Key)
	case req.Text != "":
		err = tab.Type(ctx, req.Text)
	default:
		err = errors.New("nothing to send: give mouse, key or text")
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleBrowserImport copies the person's everyday browser profile in
// again, for logins made there since the last time. Every conversation's
// tab closes, since the browser is restarted on the new copy.
func (d *Daemon) handleBrowserImport(w http.ResponseWriter, r *http.Request) {
	if d.Browser == nil {
		writeError(w, http.StatusNotFound, errors.New("this daemon has no browser"))
		return
	}
	// Only with the browser on, which is when the settings window offers
	// the button: copying somebody's whole profile is not something a
	// stray request should do while the feature is off.
	if !d.Loop.Config.BrowserLive() {
		writeError(w, http.StatusConflict, errors.New("the browser is off; turn it on first"))
		return
	}
	rep, err := d.Browser.Import()
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": rep.From, "profile": rep.Profile, "skipped": rep.Skipped})
}

// browserImport is what the last copy of the person's profile did, for
// the settings window to say, nil before the first.
func (d *Daemon) browserImport() map[string]any {
	if d.Browser == nil {
		return nil
	}
	rep := d.Browser.LastImport()
	if rep == nil {
		return nil
	}
	skipped := rep.Skipped
	if skipped == nil {
		skipped = []string{}
	}
	return map[string]any{"from": rep.From, "profile": rep.Profile, "skipped": skipped}
}

// handleSetBrowser turns the browser on or off live and persists it, with
// the same two-part answer as the Smart Agent switch.
func (d *Daemon) handleSetBrowser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	d.Loop.Config.SetBrowserRuntime(req.Enabled)
	if !req.Enabled && d.Browser != nil {
		// Off means off: the browser and every tab in it close now, not
		// when the daemon next restarts.
		d.Browser.Close()
	}
	d.announceSettings()
	resp := map[string]any{"browser": req.Enabled, "applied": true, "persisted": true}
	if d.Broker.ConfigPath != "" {
		if err := config.SetBrowserInFile(d.Broker.ConfigPath, req.Enabled); err != nil {
			resp["persisted"] = false
			resp["error"] = fmt.Sprintf("applied for this run, but failed to persist to config.json: %v", err)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// snapshotFile is the name a browser capture is saved under (see the
// browser tool's snapshotName), and nothing else: the route below serves
// files from a directory by name, and a name is all it may be given.
var snapshotFile = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9]{3,}\.png$`)

// handleSnapshot serves a capture the browser tool saved, for the
// transcript to show under the call that took it.
func (d *Daemon) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !snapshotFile.MatchString(name) {
		writeError(w, http.StatusNotFound, errors.New("no such snapshot"))
		return
	}
	id := r.PathValue("id")
	if _, err := d.Loop.Store.Get(id); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	// The same directory the tool saved into: the conversation's working
	// directory, which is its workspace or else the project.
	path := filepath.Join(agent.SnapshotDir(d.Loop.SessionDir(id)), name)
	if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, path)
		return
	}
	writeError(w, http.StatusNotFound, errors.New("no such snapshot"))
}
