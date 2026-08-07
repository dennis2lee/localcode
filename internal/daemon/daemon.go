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
	"localcode/internal/dictation"
	"localcode/internal/events"
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

	// Dictation turns microphone audio into text for the prompt box. Nil
	// when this build has no speech recognizer (every build but the
	// desktop one) or none was configured — the handlers answer with an
	// explanation rather than requiring callers to special-case it.
	Dictation *dictation.Manager

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

	// daemonEvents fans out events that belong to the daemon rather than
	// to a conversation (MCP status), which is why they bypass the
	// session event log entirely — see broadcast.go.
	daemonEvents *broadcaster
}

// New builds the daemon's HTTP handler. webFS, if non-nil, is served at "/"
// (the embedded Web UI); pass nil to run headless (TUI-only). version is
// reported back to clients via GET /api/version (e.g. for the /version
// prompt command) — it identifies the *daemon's* build, which matters when
// a TUI is attached to a remote core over --server. mcpManager may be nil
// (no MCP servers configured).
func New(loop *agent.Loop, broker *agent.PermissionBroker, tasks *agent.TaskManager, mcpManager *mcp.Manager, webFS fs.FS, version string) *Daemon {
	d := &Daemon{
		Loop:         loop,
		Broker:       broker,
		Tasks:        tasks,
		MCP:          mcpManager,
		Version:      version,
		mux:          http.NewServeMux(),
		turns:        newTurnTracker(),
		daemonEvents: newBroadcaster(),
	}
	d.routes(webFS)
	// Every status change becomes one live event carrying the whole list.
	// Registered here rather than by the caller so no wiring path can
	// forget it and leave clients with an indicator that never moves.
	if mcpManager != nil {
		mcpManager.OnStatusChange(func(states []mcp.ServerState) {
			d.daemonEvents.send(events.Event{
				Type: events.TypeMCPStatus,
				Data: map[string]any{"servers": states},
			})
		})
	}
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
	d.mux.HandleFunc("GET /api/dictation", d.handleDictationStatus)
	d.mux.HandleFunc("POST /api/dictation", d.handleDictationStart)
	d.mux.HandleFunc("POST /api/dictation/{id}/audio", d.handleDictationAudio)
	d.mux.HandleFunc("POST /api/dictation/{id}/stop", d.handleDictationStop)
	d.mux.HandleFunc("GET /api/agents", d.handleListAgents)
	d.mux.HandleFunc("GET /api/commands", d.handleListCommands)
	d.mux.HandleFunc("POST /api/sessions", d.handleCreateSession)
	d.mux.HandleFunc("GET /api/sessions", d.handleListSessions)
	d.mux.HandleFunc("GET /api/sessions/{id}", d.handleGetSession)
	d.mux.HandleFunc("DELETE /api/sessions/{id}", d.handleDeleteSession)
	d.mux.HandleFunc("DELETE /api/sessions", d.handleDeleteAllSessions)
	d.mux.HandleFunc("POST /api/sessions/{id}/agent", d.handleSwitchAgent)
	d.mux.HandleFunc("POST /api/sessions/{id}/rename", d.handleRenameSession)
	d.mux.HandleFunc("POST /api/sessions/{id}/fork", d.handleForkSession)
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

// handleListMCPServers reports every configured MCP server and its
// current state (an empty list if none are configured, or MCP itself is
// nil). Servers that failed to come up are included, as disconnected —
// leaving them out made a broken server indistinguishable from one nobody
// had configured, which is the opposite of what an indicator is for.
//
// This is the load-time read; changes after that arrive as
// events.TypeMCPStatus on the session event stream.
func (d *Daemon) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	states := []mcp.ServerState{}
	if d.MCP != nil {
		states = d.MCP.States()
	}
	writeJSON(w, http.StatusOK, states)
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
