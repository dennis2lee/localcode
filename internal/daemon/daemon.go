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
	"io/fs"
	"net/http"

	"localcode/internal/agent"
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

	// turns tracks which sessions currently have a turn in flight and how
	// to cancel each one — see turns.go.
	turns turnTracker
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
		turns:   newTurnTracker(),
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

// handleListMCPServers reports which MCP servers are currently connected
// (an empty list if none are configured, or MCP itself is nil).
func (d *Daemon) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	names := []string{}
	if d.MCP != nil {
		names = append(names, d.MCP.Servers()...)
	}
	writeJSON(w, http.StatusOK, names)
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
