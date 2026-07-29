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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"localcode/internal/agent"
	"localcode/internal/config"
	"localcode/internal/dialog"
	"localcode/internal/events"
	"localcode/internal/hooks"
	"localcode/internal/mcp"
)

type Daemon struct {
	Loop    *agent.Loop
	Broker  *agent.PermissionBroker
	Tasks   *agent.TaskManager
	Version string

	// MCP is nil when no MCP servers are configured — handleListMCPServers
	// reports an empty list in that case rather than requiring callers to
	// special-case it.
	MCP *mcp.Manager

	// PickDirectory opens a native folder picker on the machine the daemon
	// runs on, and is nil unless that machine is also the one looking at
	// the screen — i.e. only the desktop-window mode sets it. A daemon
	// reached over the network must never set it: the dialog would open on
	// the server, where nobody can answer it, and block a request until it
	// was cancelled. Clients read GET /api/workspace's "can_browse" to know
	// whether to offer the button at all.
	//
	// It returns dialog.ErrCancelled when the user dismisses the dialog.
	PickDirectory func(ctx context.Context, startDir string) (string, error)

	mux *http.ServeMux

	busyMu sync.Mutex
	busy   map[string]bool // sessionID -> a message is currently being processed
	// cancels holds the cancel func for each in-flight turn, so Esc in a
	// client (POST /api/sessions/{id}/cancel) can stop one. Guarded by
	// busyMu, since it is written and cleared at exactly the same points
	// as busy and the two must never disagree about whether a turn is
	// running.
	cancels map[string]context.CancelFunc
}

// New builds the daemon's HTTP handler. webFS, if non-nil, is served at "/"
// (the embedded Web UI); pass nil to run headless (TUI-only). version is
// reported back to clients via GET /api/version (e.g. for the /version
// prompt command) — it identifies the *daemon's* build, which matters when
// a TUI is attached to a remote core over --server. mcpManager may be nil
// (no MCP servers configured).
func New(loop *agent.Loop, broker *agent.PermissionBroker, tasks *agent.TaskManager, mcpManager *mcp.Manager, webFS fs.FS, version string) *Daemon {
	d := &Daemon{
		Loop:    loop,
		Broker:  broker,
		Tasks:   tasks,
		MCP:     mcpManager,
		Version: version,
		mux:     http.NewServeMux(),
		busy:    map[string]bool{},
		cancels: map[string]context.CancelFunc{},
	}
	d.routes(webFS)
	return d
}

func (d *Daemon) Handler() http.Handler { return d.mux }

func (d *Daemon) routes(webFS fs.FS) {
	d.mux.HandleFunc("GET /api/version", d.handleVersion)
	d.mux.HandleFunc("GET /api/settings", d.handleGetSettings)
	d.mux.HandleFunc("POST /api/settings/auto-delegate", d.handleSetAutoDelegate)
	d.mux.HandleFunc("POST /api/permissions/skip", d.handleSetSkipPermissions)
	d.mux.HandleFunc("POST /api/permissions/rules", d.handleAddPermissionRule)
	d.mux.HandleFunc("POST /api/permissions/rules/remove", d.handleRemovePermissionRule)
	d.mux.HandleFunc("GET /api/workspace", d.handleGetWorkspace)
	d.mux.HandleFunc("POST /api/workspace", d.handleSetWorkspace)
	d.mux.HandleFunc("POST /api/workspace/browse", d.handleBrowseWorkspace)
	d.mux.HandleFunc("GET /api/mcp-servers", d.handleListMCPServers)
	d.mux.HandleFunc("GET /api/agents", d.handleListAgents)
	d.mux.HandleFunc("GET /api/commands", d.handleListCommands)
	d.mux.HandleFunc("POST /api/sessions", d.handleCreateSession)
	d.mux.HandleFunc("GET /api/sessions", d.handleListSessions)
	d.mux.HandleFunc("GET /api/sessions/{id}", d.handleGetSession)
	d.mux.HandleFunc("DELETE /api/sessions/{id}", d.handleDeleteSession)
	d.mux.HandleFunc("DELETE /api/sessions", d.handleDeleteAllSessions)
	d.mux.HandleFunc("POST /api/sessions/{id}/agent", d.handleSwitchAgent)
	d.mux.HandleFunc("POST /api/sessions/{id}/rename", d.handleRenameSession)
	d.mux.HandleFunc("POST /api/sessions/{id}/messages", d.handleSendMessage)
	d.mux.HandleFunc("POST /api/sessions/{id}/uploads", d.handleUploadFile)
	d.mux.HandleFunc("GET /api/sessions/{id}/events", d.handleEvents)
	d.mux.HandleFunc("POST /api/sessions/{id}/permissions/{permId}", d.handleResolvePermission)
	d.mux.HandleFunc("POST /api/sessions/{id}/tasks", d.handleSpawnTask)
	d.mux.HandleFunc("GET /api/sessions/{id}/tasks", d.handleListTasks)
	d.mux.HandleFunc("POST /api/sessions/{id}/cancel", d.handleCancelTurn)
	d.mux.HandleFunc("POST /api/tasks/{taskId}/cancel", d.handleCancelTask)
	d.mux.HandleFunc("GET /api/tasks/{taskId}/output", d.handleTaskOutput)

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

// handleGetSettings reports the daemon's current live "/config" settings
// (process-global, not per-session) — for a client that just opened to
// know the current state without waiting for a config.changed event.
func (d *Daemon) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	skip, rules := d.Loop.Config.PermissionsSnapshot()
	// auto_delegate_agent is empty when config.json has no auto_delegate
	// block. Turning the setting on in that state is legal but does nothing,
	// so clients report that rather than showing an "on" that silently
	// delegates no prompt at all.
	delegateAgent := ""
	if d.Loop.Config.AutoDelegate != nil {
		delegateAgent = d.Loop.Config.AutoDelegate.Agent
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"auto_compact_enabled": d.Loop.AutoCompactEnabled(),
		"show_tps":             d.Loop.ShowTPS(),
		"auto_delegate":        d.Loop.AutoDelegateEnabled(),
		"auto_delegate_agent":  delegateAgent,
		"skip_permissions":     skip,
		"permission_rules":     rules,
		"can_edit_permissions": d.Broker.ConfigPath != "",
	})
}

// handleSetAutoDelegate toggles auto-delegation live (same setting "/config
// auto_delegate on|off" changes) and, when a config.json path is known,
// persists it so the choice survives a restart. The runtime change lands
// first and is never rolled back by a persistence failure: a caller that
// asked for the setting to apply "right now" gets that, and the error tells
// them only the saving part failed.
func (d *Daemon) handleSetAutoDelegate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	d.Loop.SetAutoDelegateEnabled(req.Enabled)
	if d.Broker.ConfigPath != "" {
		if err := config.SetAutoDelegateEnabledInFile(d.Broker.ConfigPath, req.Enabled); err != nil {
			http.Error(w, fmt.Sprintf("applied for this run, but failed to persist to config.json: %v", err), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSetSkipPermissions toggles skip_permissions at runtime and, when a
// config.json path is known (see Broker.ConfigPath, the same path "always
// allow" writes to), persists it so the setting survives a restart.
func (d *Daemon) handleSetSkipPermissions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	d.Loop.Config.SetSkipPermissionsRuntime(req.Enabled)
	if d.Broker.ConfigPath != "" {
		if err := config.SetSkipPermissionsInFile(d.Broker.ConfigPath, req.Enabled); err != nil {
			http.Error(w, fmt.Sprintf("saved for this run, but failed to persist to config.json: %v", err), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// permissionRuleRequest is the body for both add and remove: which tool,
// and the exact rule (match pattern + decision) to add or remove.
type permissionRuleRequest struct {
	Tool     string `json:"tool"`
	Match    string `json:"match"`
	Decision string `json:"decision"`
}

func (d *Daemon) handleAddPermissionRule(w http.ResponseWriter, r *http.Request) {
	var req permissionRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Tool == "" || req.Match == "" || req.Decision == "" {
		http.Error(w, "tool, match, and decision are all required", http.StatusBadRequest)
		return
	}
	rule := config.PermissionRule{Match: req.Match, Decision: config.Decision(req.Decision)}
	d.Loop.Config.AddPermissionRuleRuntime(req.Tool, rule)
	if d.Broker.ConfigPath != "" {
		if err := config.AddPermissionRuleToFile(d.Broker.ConfigPath, req.Tool, rule); err != nil {
			http.Error(w, fmt.Sprintf("saved for this run, but failed to persist to config.json: %v", err), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d *Daemon) handleRemovePermissionRule(w http.ResponseWriter, r *http.Request) {
	var req permissionRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Tool == "" || req.Match == "" || req.Decision == "" {
		http.Error(w, "tool, match, and decision are all required", http.StatusBadRequest)
		return
	}
	rule := config.PermissionRule{Match: req.Match, Decision: config.Decision(req.Decision)}
	d.Loop.Config.RemovePermissionRuleRuntime(req.Tool, rule)
	if d.Broker.ConfigPath != "" {
		if err := config.RemovePermissionRuleFromFile(d.Broker.ConfigPath, req.Tool, rule); err != nil {
			http.Error(w, fmt.Sprintf("saved for this run, but failed to persist to config.json: %v", err), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

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

	d.busyMu.Lock()
	for _, b := range d.busy {
		if b {
			d.busyMu.Unlock()
			http.Error(w, "a turn is in progress; cancel or wait for it before switching workspace", http.StatusConflict)
			return
		}
	}
	d.busyMu.Unlock()

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

// handleListMCPServers reports which MCP servers are currently connected
// (an empty list if none are configured, or MCP itself is nil).
func (d *Daemon) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	names := []string{}
	if d.MCP != nil {
		names = append(names, d.MCP.Servers()...)
	}
	writeJSON(w, http.StatusOK, names)
}

// AgentInfo is the client-facing view of a configured agent — enough to
// build a picker (TUI Tab-cycle, Web UI dropdown) without exposing the
// full config.AgentConfig (system prompt, tool list). Model is resolved
// from the agent's profile so clients can show e.g. "agent: explore ·
// model: qwen3-30b-a3b" without needing their own copy of config.json.
type AgentInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Model       string `json:"model,omitempty"`
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
		agentCfg := d.Loop.Config.Agents[name]
		info := AgentInfo{Name: name, Description: agentCfg.Description}
		if profile, ok := d.Loop.Config.Profiles[agentCfg.Profile]; ok {
			info.Model = profile.Model
		}
		out = append(out, info)
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

// handleRenameSession sets a session's cosmetic Title (session picker
// display only — resolution/resumption is always by ID).
func (d *Daemon) handleRenameSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := d.Loop.Store.Get(id); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	var req struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	sess, err := d.Loop.Store.SetTitle(id, req.Title)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	d.Loop.Store.Append(id, events.TypeSessionRenamed, map[string]any{"title": req.Title})

	writeJSON(w, http.StatusOK, sess)
}

// handleDeleteSession removes a session (and its persisted log, if any)
// entirely. Refuses to delete a session with an in-flight turn (the same
// busy guard handleSendMessage uses) so a running turn never writes to a
// session whose file handle was just closed out from under it.
func (d *Daemon) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	d.busyMu.Lock()
	busy := d.busy[id]
	d.busyMu.Unlock()
	if busy {
		writeError(w, http.StatusConflict, fmt.Errorf("session %s has a turn in progress", id))
		return
	}

	if err := d.Loop.Store.Delete(id); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	d.Loop.ClearSessionState(id)
	d.Broker.ForgetSession(id)
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteAllSessions wipes every session (visible and background-task
// children alike) — the "delete all" bulk action. Refuses if ANY session
// has a turn in-flight, same guard as a single delete, so a running turn
// never writes to a session whose file handle just got closed out from
// under it.
func (d *Daemon) handleDeleteAllSessions(w http.ResponseWriter, r *http.Request) {
	sessions := d.Loop.Store.AllSessions()

	d.busyMu.Lock()
	var busyIDs []string
	for _, s := range sessions {
		if d.busy[s.ID] {
			busyIDs = append(busyIDs, s.ID)
		}
	}
	d.busyMu.Unlock()
	if len(busyIDs) > 0 {
		writeError(w, http.StatusConflict, fmt.Errorf("sessions with a turn in progress: %v", busyIDs))
		return
	}

	if err := d.Loop.Store.DeleteAll(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for _, s := range sessions {
		d.Loop.ClearSessionState(s.ID)
		d.Broker.ForgetSession(s.ID)
	}
	w.WriteHeader(http.StatusNoContent)
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
	// Stamped with the workspace live at creation time. Switching the
	// workspace later doesn't rewrite existing sessions: the point of the
	// field is to say which project a conversation belongs to, which is
	// exactly what would be lost by keeping them all in sync.
	sess, err := d.Loop.Store.CreateSessionIn(id, "", req.Agent, d.Loop.GetProjectDir(), true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if len(d.Loop.Config.Hooks) > 0 {
		// Fire-and-forget: session_start is purely a notification point
		// (e.g. log/announce a new session starting) — nothing to block.
		hooks.Run(r.Context(), d.Loop.Config.Hooks, hooks.EventSessionStart, map[string]any{
			"session_id": id,
			"agent":      req.Agent,
		})
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

	// Deliberately rooted at context.Background(), not r.Context(): the HTTP
	// request returns immediately (202) and must not cancel the turn when
	// the client disconnects. It is cancellable only on purpose, via
	// handleCancelTurn.
	turnCtx, cancel := context.WithCancel(context.Background())

	d.busyMu.Lock()
	if d.busy[id] {
		d.busyMu.Unlock()
		cancel()
		writeError(w, http.StatusConflict, fmt.Errorf("session %s is already processing a message", id))
		return
	}
	d.busy[id] = true
	d.cancels[id] = cancel
	d.busyMu.Unlock()

	go func() {
		err := d.Loop.SendMessage(turnCtx, id, sess.Agent, req.Text)

		// Read the cancellation state BEFORE calling cancel() below —
		// cancel() makes turnCtx.Err() non-nil unconditionally, so
		// checking it afterwards would classify every successful turn as
		// user-cancelled.
		wasCancelled := turnCtx.Err() != nil

		// Clear busy BEFORE appending the terminal event. Clients send
		// their next (possibly queued) message the moment they see it, so
		// the other order is a race: event observed, busy still set, 409.
		cancel()
		d.busyMu.Lock()
		delete(d.busy, id)
		delete(d.cancels, id)
		d.busyMu.Unlock()

		// A cancelled turn is a user action, not a failure: record it as
		// its own event so clients can drop the spinner without showing an
		// error. Checking the context rather than the error keeps this
		// correct no matter which layer noticed the cancellation first.
		if wasCancelled {
			d.Loop.Store.Append(id, events.TypeTurnCancelled, map[string]any{})
			return
		}
		if err != nil {
			log.Printf("session %s: SendMessage: %v", id, err)
		}
		// The turn boundary clients act on. message.part.end is NOT that
		// boundary — it fires per model message, and a turn with tool
		// calls has several, which is exactly what used to make clients
		// think the turn was over mid-tool and 409 on their next send.
		// Emitted on the error path too (the error event itself already
		// told the user what went wrong; this just marks the turn over).
		d.Loop.Store.Append(id, events.TypeTurnDone, map[string]any{})
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
		// Scope is one of "once" (default), "session", or "always". See
		// agent.PermissionBroker.Resolve.
		Scope string `json:"scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	d.Broker.Resolve(permID, req.Allow, req.Scope)
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

// handleCancelTurn stops the turn currently running for a session, the
// endpoint behind Esc in the clients. Cancelling when nothing is running
// is not an error, just {"cancelled": false} — a user mashing Esc at an
// idle prompt should not see a failure.
func (d *Daemon) handleCancelTurn(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	d.busyMu.Lock()
	cancel, running := d.cancels[id]
	d.busyMu.Unlock()

	if running {
		// The turn's own goroutine clears busy/cancels and records the
		// turn.cancelled event, so this only has to pull the trigger.
		cancel()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": running})
}

// handleTaskOutput returns everything a background task's model has said
// so far — a task is a session, so this reads its event log and works
// mid-run, which is what makes "/tasks <id>" useful as a progress view
// rather than only a post-mortem.
func (d *Daemon) handleTaskOutput(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("taskId")
	evs, err := d.Loop.Store.Events(taskID, 0)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown task %s", taskID))
		return
	}
	var out strings.Builder
	for _, ev := range evs {
		switch ev.Type {
		case events.TypeMessagePartDelta:
			if text, ok := ev.Data["text"].(string); ok {
				out.WriteString(text)
			}
		case events.TypeMessagePartEnd:
			out.WriteString("\n")
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": strings.TrimRight(out.String(), "\n")})
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
