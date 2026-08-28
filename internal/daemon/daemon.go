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
	"io"
	"io/fs"
	"log"
	"net/http"

	"localcode/internal/agent"
	"localcode/internal/events"
	"localcode/internal/mcp"
	"sync"
)

type Daemon struct {
	Loop    *agent.Loop
	Broker  *agent.PermissionBroker
	Tasks   *agent.TaskManager
	Version string

	// mcpMu guards MCP, which "/reset-mcp" can swap while requests are
	// reading it.
	mcpMu sync.Mutex
	// MCP is nil when no MCP servers are configured — handleListMCPServers
	// reports an empty list in that case rather than requiring callers to
	// special-case it.
	MCP *mcp.Manager

	// AllowUpdateInstall lets this daemon download a new release and start
	// the platform's installer. Set only where the daemon and the person
	// clicking share a machine — the desktop window — for the same reason
	// PickDirectory is: over --server, the button would replace the program
	// on the *server*, at the request of a browser somewhere else. Checking
	// for a release is not gated; only installing one is.
	AllowUpdateInstall bool
	// Restart brings this program back up on the binary that has just
	// replaced it. Nil where that cannot be done — a daemon someone
	// reached over the network is not one to restart from a browser, and
	// on Windows the installer owns the replacement — and where it is nil
	// the update is reported as installed with a restart left to the user,
	// which is what every install did before this existed.
	//
	// It is called after the response has gone out, because on Unix it
	// replaces this process image and nothing after it runs.
	Restart func()
	// UpdateAPI overrides GitHub's API address for the update check. Empty
	// in every real build; set by tests.
	UpdateAPI string

	// ConfigPath is the config.json settings changes are written to, or
	// "" when this daemon was started without one. Empty is not an
	// error: a live setting still applies for as long as the process
	// runs, and a panel that says so is better than a control that
	// silently forgets.
	ConfigPath string

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

	// RevealDirectory opens a directory in the machine's file manager. Set
	// and left nil under exactly the same rule as PickDirectory, and for
	// the same reason: a window opened on a machine nobody is looking at
	// helps nobody. Clients read "can_reveal" from GET /api/workspace.
	RevealDirectory func(ctx context.Context, dir string) error

	// Policy answers the four permission switches for a session. Built
	// here from the loop's own store and config, so the daemon and the
	// tool registry cannot disagree about what a session has said.
	Policy *agent.PermissionPolicy

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
	// The agent loop asks for mid-turn input at every tool call; the queue
	// itself lives here, because only the daemon knows whether a turn is
	// still registered as running. Wired at construction so no path can
	// build a daemon whose loop silently ignores what someone typed.
	loop.PendingInput = d.turns.takeOne
	loop.UserWaiting = d.turns.hasPending

	// The broker needs the switches for the "allow anywhere outside this
	// project" answer, which turns one on rather than remembering
	// something only it can see, and the daemon needs to be told so a
	// window showing the switches follows an answer given at the prompt.
	d.Policy = agent.NewPermissionPolicy(loop.Store, loop.Config)
	broker.SetPolicy(d.Policy, d.announceSessionPermissions)
	loop.OnPermissionsChanged = d.announceSessionPermissions
	loop.ForgetOutside = broker.ForgetOutside

	d.routes(webFS)
	// Every status change becomes one live event carrying the whole list.
	// Registered here rather than by the caller so no wiring path can
	// forget it and leave clients with an indicator that never moves.
	// One event per session whose turn starts or ends, so the session
	// list can show which conversations are working without polling —
	// and, more to the point, which one is holding up a workspace change.
	d.turns.onChange = func(sessionID string, busy bool) {
		d.daemonEvents.send(events.Event{
			Type: events.TypeSessionActivity,
			Data: map[string]any{"session": sessionID, "busy": busy},
		})
	}
	// One place every switch change becomes an event, wired here so no
	// path that changes a setting can forget it: the toggle commands
	// call the loop's hook, and the settings endpoints call this method
	// directly.
	loop.OnSettingsChanged = d.announceSettings
	if mcpManager != nil {
		d.watchMCP(mcpManager)
	}
	return d
}

func (d *Daemon) Handler() http.Handler { return d.mux }

func (d *Daemon) routes(webFS fs.FS) {
	d.mux.HandleFunc("GET /api/version", d.handleVersion)
	d.mux.HandleFunc("GET /api/update", d.handleUpdateCheck)
	d.mux.HandleFunc("POST /api/update/install", d.handleUpdateInstall)
	d.mux.HandleFunc("GET /api/trace", d.handleTrace)
	d.mux.HandleFunc("GET /api/settings", d.handleGetSettings)
	d.mux.HandleFunc("POST /api/settings/auto-delegate", d.handleSetAutoDelegate)
	d.mux.HandleFunc("POST /api/settings/smart-agent", d.handleSetSmartAgent)
	d.mux.HandleFunc("POST /api/settings/keep-going", d.handleSetKeepGoing)
	d.mux.HandleFunc("POST /api/permissions/skip", d.handleSetSkipPermissions)
	d.mux.HandleFunc("POST /api/permissions/rules", d.handleAddPermissionRule)
	d.mux.HandleFunc("POST /api/permissions/rules/remove", d.handleRemovePermissionRule)
	d.mux.HandleFunc("GET /api/workspace", d.handleGetWorkspace)
	d.mux.HandleFunc("POST /api/workspace", d.handleSetWorkspace)
	d.mux.HandleFunc("POST /api/workspace/browse", d.handleBrowseWorkspace)
	d.mux.HandleFunc("POST /api/workspace/reveal", d.handleRevealWorkspace)
	d.mux.HandleFunc("GET /api/mcp-servers", d.handleListMCPServers)
	d.mux.HandleFunc("GET /api/agents", d.handleListAgents)
	d.mux.HandleFunc("GET /api/commands", d.handleListCommands)
	d.mux.HandleFunc("GET /api/skills", d.handleListSkills)
	d.mux.HandleFunc("GET /api/slash-commands", d.handleListSlashCommands)
	d.mux.HandleFunc("POST /api/sessions", d.handleCreateSession)
	d.mux.HandleFunc("GET /api/sessions", d.handleListSessions)
	d.mux.HandleFunc("GET /api/sessions/{id}", d.handleGetSession)
	d.mux.HandleFunc("DELETE /api/sessions/{id}", d.handleDeleteSession)
	d.mux.HandleFunc("DELETE /api/sessions", d.handleDeleteAllSessions)
	d.mux.HandleFunc("POST /api/sessions/order", d.handleReorderSessions)
	d.mux.HandleFunc("POST /api/sessions/{id}/agent", d.handleSwitchAgent)
	d.mux.HandleFunc("POST /api/sessions/{id}/rename", d.handleRenameSession)
	d.mux.HandleFunc("POST /api/sessions/{id}/fork", d.handleForkSession)
	d.mux.HandleFunc("POST /api/sessions/{id}/messages", d.handleSendMessage)
	d.mux.HandleFunc("POST /api/sessions/{id}/uploads", d.handleUploadFile)
	d.mux.HandleFunc("GET /api/sessions/{id}/events", d.handleEvents)
	// The four switches, per session. Registered before the pattern that
	// takes a {permId}, which they cannot collide with (Go's mux matches
	// the more specific literal), but read together they are two
	// different things at one path: one answers a pending question, the
	// other configures which questions get asked.
	d.mux.HandleFunc("GET /api/sessions/{id}/permissions", d.handleGetSessionPermissions)
	d.mux.HandleFunc("POST /api/sessions/{id}/permissions", d.handleSetSessionPermission)
	d.mux.HandleFunc("POST /api/sessions/{id}/permissions/forget", d.handleForgetSessionOutside)
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

// announceSettings tells every connected client what the daemon-wide
// switches are now, whoever just moved one.
//
// A snapshot of all of them rather than the one that changed, so a
// client applies a state instead of merging a sequence and cannot end up
// half-updated by an event it missed. On the broadcast rather than in a
// session's log, because a setting belongs to the daemon and not to any
// one conversation; a client that was not connected re-reads
// GET /api/settings on load, which is the same shape.
func (d *Daemon) announceSettings() {
	d.daemonEvents.send(events.Event{
		Type: events.TypeSettingsChanged,
		Data: map[string]any{
			"auto_compact_enabled": d.Loop.AutoCompactEnabled(),
			"show_tps":             d.Loop.ShowTPS(),
			"auto_delegate":        d.Loop.AutoDelegateEnabled(),
			"smart_agent":          d.Loop.SmartAgentEnabled(),
			"skip_permissions":     d.Loop.Config.PermissionsSkipped(),
			"keep_going":           d.Loop.KeepGoingEnabled(),
			"auto_compact_percent": d.Loop.CompactPercent(),
		},
	})
}

// watchMCP forwards a manager's status changes to every connected
// client. Split out of New because "/reset-mcp" builds a new manager
// mid-run, and the new one needs the same wiring or the indicator stops
// moving exactly when somebody is watching it.
func (d *Daemon) watchMCP(m *mcp.Manager) {
	m.OnStatusChange(func(states []mcp.ServerState) {
		d.daemonEvents.send(events.Event{
			Type: events.TypeMCPStatus,
			Data: map[string]any{"servers": states},
		})
	})
}

// SwapMCP replaces the manager "/reset-mcp" just rebuilt, wires its
// status changes to clients, and announces the new state so every
// indicator moves without being asked.
func (d *Daemon) SwapMCP(m *mcp.Manager) {
	d.mcpMu.Lock()
	d.MCP = m
	d.mcpMu.Unlock()
	if m != nil {
		d.watchMCP(m)
		d.daemonEvents.send(events.Event{
			Type: events.TypeMCPStatus,
			Data: map[string]any{"servers": m.States()},
		})
	}
}

// mcpManager reads the current manager under the same lock SwapMCP
// writes it with.
func (d *Daemon) mcpManager() *mcp.Manager {
	d.mcpMu.Lock()
	defer d.mcpMu.Unlock()
	return d.MCP
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
	if m := d.mcpManager(); m != nil {
		states = m.States()
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

// maxJSONBody caps a JSON request body.
//
// A prompt is the largest thing that legitimately arrives here, and a
// megabyte of it is far beyond any conversation — the file-attach path
// has its own, larger limit and does not come through these handlers.
//
// The cap matters because `--listen` can bind something other than
// loopback: without it, `POST /api/sessions/{id}/messages` with a
// multi-gigabyte "text" is allocated in full before the empty check, and
// on success it is written into the session log. uploads.go already
// capped its own, so this was an inconsistency rather
// than a decision.
const maxJSONBody = 1 << 20

// jsonBody wraps a request body in that cap. Reading past it fails the
// decode, which every caller already handles as a bad request.
func jsonBody(w http.ResponseWriter, r *http.Request) io.Reader {
	return http.MaxBytesReader(w, r.Body, maxJSONBody)
}

// httpError carries a status alongside a message, so work done inside a
// closure (see turnTracker.whileIdle) can still choose the response it
// would have written itself.
type httpError struct {
	status  int
	message string
}

func (e *httpError) Error() string { return e.message }

func writeHTTPError(w http.ResponseWriter, err error) {
	var he *httpError
	if errors.As(err, &he) {
		http.Error(w, he.message, he.status)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// logAppend reports an event that could not be recorded.
//
// These appends shape what clients show — the TUI takes the current agent
// only from the event, not from the HTTP reply — so one that silently
// fails leaves the footer naming an agent the daemon is no longer using,
// with nothing anywhere to say so. Not fatal: the change itself has
// already happened, and refusing the request afterwards would be a
// second, invented failure. But it must not be invisible.
func logAppend(sessionID string, typ events.Type, err error) {
	if err != nil {
		log.Printf("session %s: could not record %s: %v", sessionID, typ, err)
	}
}
