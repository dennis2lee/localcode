package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	// A backend that is not there answers 502 with no body, and that is
	// what every request looked like after a successor died: "POST
	// /api/sessions/…/agent: 502", with nothing to say which process was
	// gone or where to look.
	//
	// Naming the log file was the first answer and it was not enough: the
	// person reading this is looking at a transcript, and the file has now
	// been asked for twice without arriving. So when the process really has
	// gone, its own last words are in the reply — a Go panic's first frames,
	// a config error, whatever it printed — and the 502 that reaches the
	// screen is the diagnosis rather than a pointer to one.
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		msg := fmt.Sprintf("the localcode behind this window is not answering at %s (%v).", target, err)
		if last := successorEpitaph(); last != "" {
			msg += last
		} else {
			// Still running, as far as this process knows, so this is not
			// a death to report: say where to look and leave it there.
			msg += fmt.Sprintf(" It was started by an update; what it says goes to %s.", handoffLogPath())
		}
		writeJSONError(w, http.StatusBadGateway, msg+" Reopen the window to run this version directly.")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/workspace/browse", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Start string `json:"start"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		path, err := pickDirectory(r.Context(), "Choose a workspace folder", req.Start)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]any{"path": path})
	})
	// Which folder to open is asked of the daemon, never read from the
	// request.
	//
	// The page sends no path here and must not: this starts a process with
	// a path argument, and taking that argument from the caller would make
	// the folder button a way to ask the window to open anything at all.
	// The daemon's own handler refuses a caller-supplied path for exactly
	// that reason and resolves the session's workspace itself
	// (Daemon.handleRevealWorkspace); this copy had drifted into reading
	// {"path": ...} out of the body, which reintroduced it and never
	// worked either — there is no path in that body to read. Every click
	// on the folder icon, in a window that had taken an update, answered
	// 500 "no workspace directory to open".
	mux.HandleFunc("POST /api/workspace/reveal", func(w http.ResponseWriter, r *http.Request) {
		dir, err := successorWorkspace(r.Context(), target, r.URL.Query().Get("session"))
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Not the request's context: this waits on explorer.exe, and
		// cancelling it when the reply is written would kill the process
		// that is opening the window.
		if err := revealDirectory(context.Background(), dir); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]any{"path": dir})
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

// The two native dialogs behind variables, so a test can drive these
// routes without a folder picker and a file-manager window opening on
// the machine running the suite. They are also the two routes this
// process keeps for itself, and therefore the two the successor cannot
// be asked to check.
var (
	revealDirectory = dialog.RevealDirectory
	pickDirectory   = dialog.PickDirectory
)

// successorWorkspace asks the daemon behind the window which directory a
// session is working in.
//
// A request of its own rather than one routed through the proxy: the proxy
// carries the page's requests, and this is the window asking a question
// the page never asked.
func successorWorkspace(ctx context.Context, target, session string) (string, error) {
	u := "http://" + target + "/api/workspace"
	if session != "" {
		u += "?session=" + url.QueryEscape(session)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ask the localcode behind this window where the workspace is: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the localcode behind this window answered %s when asked where the workspace is", resp.Status)
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("read the workspace from the localcode behind this window: %w", err)
	}
	if body.Path == "" {
		return "", fmt.Errorf("the localcode behind this window reports no workspace for this conversation")
	}
	return body.Path, nil
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
