package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httputil"
	"net/url"

	"localcode/internal/dialog"
)

// The desktop window's startup handoff.
//
// The window serves whatever handler it is given on a private loopback
// port, and it used to be given the daemon built in its own process.
// After a startup update the daemon is the new binary in another
// process, so the window is given a proxy to it instead. Everything the
// page does — the API, the event stream, the static files — goes
// through, and the static files are the new version's, so the page is
// the new interface even though the window shell is not.
//
// Two routes stay in this process, because they open native dialogs and
// a dialog has to belong to the window the person is looking at, not to
// a daemon with no window: choosing a workspace folder, and revealing
// one in the file manager. GET /api/workspace is rewritten so the page
// knows both are available, which the daemon behind the proxy would
// have said they are not.
func successorProxy(target string) http.Handler {
	u, err := url.Parse("http://" + target)
	if err != nil {
		panic("successorProxy: " + err.Error())
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	// The event stream never ends and is written a line at a time; a
	// proxy that buffered it would show a reply only when it finished.
	proxy.FlushInterval = -1

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/workspace/browse", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Start string `json:"start"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		path, err := dialog.PickDirectory(r.Context(), "Choose a workspace folder", req.Start)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]any{"path": path})
	})
	mux.HandleFunc("POST /api/workspace/reveal", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if err := dialog.RevealDirectory(context.Background(), req.Path); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]any{"path": req.Path})
	})
	mux.HandleFunc("GET /api/workspace", func(w http.ResponseWriter, r *http.Request) {
		// Asked of the daemon, then corrected: the workspace is its
		// answer, the two abilities are this process's.
		rw := &captured{header: http.Header{}}
		proxy.ServeHTTP(rw, r)
		var body map[string]any
		if rw.status == http.StatusOK && json.Unmarshal(rw.body, &body) == nil {
			body["can_browse"] = true
			body["can_reveal"] = true
			writeJSON(w, body)
			return
		}
		for k, v := range rw.header {
			w.Header()[k] = v
		}
		w.WriteHeader(rw.status)
		w.Write(rw.body)
	})
	mux.Handle("/", proxy)
	return mux
}

// captured is a ResponseWriter that keeps the answer so it can be edited
// before it is sent on.
type captured struct {
	header http.Header
	status int
	body   []byte
}

func (c *captured) Header() http.Header { return c.header }
func (c *captured) WriteHeader(code int) {
	c.status = code
}
func (c *captured) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	c.body = append(c.body, b...)
	return len(b), nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
