package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"localcode/internal/events"
	"localcode/internal/hooks"
)

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

// handleDeleteSession removes a session (and its persisted log, if any)
// entirely. Refuses to delete a session with an in-flight turn (the same
// busy guard handleSendMessage uses) so a running turn never writes to a
// session whose file handle was just closed out from under it.
func (d *Daemon) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if d.turns.busy(id) {
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

	ids := make([]string, len(sessions))
	for i, s := range sessions {
		ids[i] = s.ID
	}
	if busyIDs := d.turns.anyBusy(ids); len(busyIDs) > 0 {
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
