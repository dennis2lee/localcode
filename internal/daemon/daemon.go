// Package daemon exposes the core agent loop over HTTP + Server-Sent
// Events, so the TUI and a Web UI can be equal, independent clients of the
// same running session instead of the TUI calling agent.Loop in-process.
//
// Session state lives entirely on the server: clients never hold
// conversation history themselves, only a `since` sequence number they use
// to resume the event stream.
package daemon

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"localcode/internal/agent"
	"localcode/internal/events"
)

type Daemon struct {
	Loop    *agent.Loop
	Broker  *agent.PermissionBroker
	Tasks   *agent.TaskManager
	Version string

	mux *http.ServeMux

	busyMu sync.Mutex
	busy   map[string]bool // sessionID -> a message is currently being processed
}

// New builds the daemon's HTTP handler. webFS, if non-nil, is served at "/"
// (the embedded Web UI); pass nil to run headless (TUI-only). version is
// reported back to clients via GET /api/version (e.g. for the /version
// prompt command) — it identifies the *daemon's* build, which matters when
// a TUI is attached to a remote core over --server.
func New(loop *agent.Loop, broker *agent.PermissionBroker, tasks *agent.TaskManager, webFS fs.FS, version string) *Daemon {
	d := &Daemon{
		Loop:    loop,
		Broker:  broker,
		Tasks:   tasks,
		Version: version,
		mux:     http.NewServeMux(),
		busy:    map[string]bool{},
	}
	d.routes(webFS)
	return d
}

func (d *Daemon) Handler() http.Handler { return d.mux }

func (d *Daemon) routes(webFS fs.FS) {
	d.mux.HandleFunc("GET /api/version", d.handleVersion)
	d.mux.HandleFunc("GET /api/agents", d.handleListAgents)
	d.mux.HandleFunc("GET /api/commands", d.handleListCommands)
	d.mux.HandleFunc("POST /api/sessions", d.handleCreateSession)
	d.mux.HandleFunc("GET /api/sessions", d.handleListSessions)
	d.mux.HandleFunc("GET /api/sessions/{id}", d.handleGetSession)
	d.mux.HandleFunc("POST /api/sessions/{id}/agent", d.handleSwitchAgent)
	d.mux.HandleFunc("POST /api/sessions/{id}/messages", d.handleSendMessage)
	d.mux.HandleFunc("GET /api/sessions/{id}/events", d.handleEvents)
	d.mux.HandleFunc("POST /api/sessions/{id}/permissions/{permId}", d.handleResolvePermission)
	d.mux.HandleFunc("POST /api/sessions/{id}/tasks", d.handleSpawnTask)
	d.mux.HandleFunc("GET /api/sessions/{id}/tasks", d.handleListTasks)
	d.mux.HandleFunc("POST /api/tasks/{taskId}/cancel", d.handleCancelTask)

	if webFS != nil {
		d.mux.Handle("/", http.FileServerFS(webFS))
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (d *Daemon) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": d.Version})
}

// AgentInfo is the client-facing view of a configured agent — enough to
// build a picker (TUI Tab-cycle, Web UI dropdown) without exposing the
// full config.AgentConfig (system prompt, tool list).
type AgentInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// handleListAgents returns every agent defined in config.json's agents
// map, sorted by name — the picklist for switching a session's active
// agent (e.g. plan -> build).
func (d *Daemon) handleListAgents(w http.ResponseWriter, r *http.Request) {
	names := make([]string, 0, len(d.Loop.Config.Agents))
	for name := range d.Loop.Config.Agents {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]AgentInfo, 0, len(names))
	for _, name := range names {
		out = append(out, AgentInfo{Name: name, Description: d.Loop.Config.Agents[name].Description})
	}
	writeJSON(w, http.StatusOK, out)
}

// CommandInfo is the client-facing view of a loaded custom slash command.
type CommandInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// handleListCommands returns every custom command loaded from
// .localcode/commands/*.md (project) and ~/.localcode/commands/*.md
// (global) — for a /help listing or client-side autocomplete. Actually
// running a command still goes through POST .../messages like any other
// message; the server matches "/<name>" there.
func (d *Daemon) handleListCommands(w http.ResponseWriter, r *http.Request) {
	out := make([]CommandInfo, 0, len(d.Loop.Commands))
	for _, c := range d.Loop.Commands {
		out = append(out, CommandInfo{Name: c.Name, Description: c.Description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

// handleSwitchAgent changes which agent a session sends future messages
// as — mid-conversation history is untouched, only the model/system
// prompt/tool scope used for the *next* message changes. This is what
// backs Tab-cycling in the TUI (plan -> build) or the Web UI's agent
// selector, and the /agent slash command in both.
func (d *Daemon) handleSwitchAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := d.Loop.Store.Get(id); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	var req struct {
		Agent string `json:"agent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if _, ok := d.Loop.Config.Agents[req.Agent]; !ok {
		writeError(w, http.StatusBadRequest, fmt.Errorf("unknown agent %q", req.Agent))
		return
	}

	sess, err := d.Loop.Store.SetAgent(id, req.Agent)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	d.Loop.Store.Append(id, events.TypeAgentSwitched, map[string]any{"agent": req.Agent})

	writeJSON(w, http.StatusOK, sess)
}

func (d *Daemon) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Agent string `json:"agent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Agent == "" {
		req.Agent = "general-purpose"
	}

	id := fmt.Sprintf("s-%d", time.Now().UnixNano())
	sess, err := d.Loop.Store.CreateSession(id, "", req.Agent, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

// handleListSessions returns every top-level (visible) session, newest
// first, so a client can offer "resume an existing session" instead of
// always starting a new one.
func (d *Daemon) handleListSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.Loop.Store.ListVisible())
}

func (d *Daemon) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := d.Loop.Store.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (d *Daemon) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := d.Loop.Store.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("text is required"))
		return
	}

	d.busyMu.Lock()
	if d.busy[id] {
		d.busyMu.Unlock()
		writeError(w, http.StatusConflict, fmt.Errorf("session %s is already processing a message", id))
		return
	}
	d.busy[id] = true
	d.busyMu.Unlock()

	go func() {
		defer func() {
			d.busyMu.Lock()
			delete(d.busy, id)
			d.busyMu.Unlock()
		}()
		// Deliberately not r.Context(): the HTTP request returns immediately
		// (202) and must not cancel the turn when the client disconnects.
		if err := d.Loop.SendMessage(context.Background(), id, sess.Agent, req.Text); err != nil {
			log.Printf("session %s: SendMessage: %v", id, err)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// handleEvents streams the session's event log as SSE: any backlog since
// the given seq first, then live events. Subscribing before reading the
// backlog (rather than after) means the only failure mode is a duplicate
// event across the two sources, never a gap — duplicates are filtered by
// seq before being written to the client.
func (d *Daemon) handleEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	since := uint64(0)
	if s := r.URL.Query().Get("since"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid since: %w", err))
			return
		}
		since = v
	} else if lastEventID := r.Header.Get("Last-Event-ID"); lastEventID != "" {
		// Browsers' EventSource auto-reconnects on a dropped connection and
		// resends whatever `id:` value the server last sent as this
		// header, so a client that never set ?since= explicitly still
		// resumes without re-fetching events it already has.
		if v, err := strconv.ParseUint(lastEventID, 10, 64); err == nil {
			since = v
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}

	live, unsub, err := d.Loop.Store.Subscribe(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	defer unsub()

	backlog, err := d.Loop.Store.Events(id, since)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush() // send headers immediately even if there's no backlog to write yet

	lastSeq := since
	writeSSE := func(ev events.Event) {
		if ev.Seq <= lastSeq {
			return // already sent via backlog or an earlier live event
		}
		lastSeq = ev.Seq
		payload, err := json.Marshal(ev)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.Seq, payload)
		flusher.Flush()
	}

	for _, ev := range backlog {
		writeSSE(ev)
	}

	for {
		select {
		case ev, ok := <-live:
			if !ok {
				return
			}
			writeSSE(ev)
		case <-r.Context().Done():
			return
		}
	}
}

func (d *Daemon) handleResolvePermission(w http.ResponseWriter, r *http.Request) {
	permID := r.PathValue("permId")
	var req struct {
		Allow bool `json:"allow"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	d.Broker.Resolve(permID, req.Allow)
	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

func (d *Daemon) handleSpawnTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Agent  string `json:"agent"`
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

func (d *Daemon) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskId")
	ok := d.Tasks.Cancel(taskID)
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": ok})
}

//go:embed all:static
var embeddedWebFS embed.FS

// WebFS returns the embedded Web UI's filesystem rooted at the static
// directory (so "/" maps to static/index.html).
func WebFS() fs.FS {
	sub, err := fs.Sub(embeddedWebFS, "static")
	if err != nil {
		panic(err) // programmer error: static/ must exist at build time
	}
	return sub
}
