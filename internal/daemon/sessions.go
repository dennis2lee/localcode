package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"localcode/internal/events"
	"localcode/internal/hooks"
	"localcode/internal/session"
)

func (d *Daemon) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Agent string `json:"agent"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
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

// handleForkSession starts a new session carrying a copy of another
// session's conversation, so a promising thread can be taken in two
// directions without either branch disturbing the other.
//
// The copy is the event log, verbatim: it is the single source of both
// what a client replays into the transcript and what RehydrateSession
// rebuilds the model's history from, so copying it gets both at once and
// keeps them consistent by construction.
//
// Refused while the source has a turn in flight. That is not politeness:
// a log caught mid-turn can end after a tool call was requested but
// before its result was recorded, and a history with a dangling tool call
// is one the provider rejects outright on the fork's very first message.
func (d *Daemon) handleForkSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	src, err := d.Loop.Store.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	// The log is read with turns held off, not merely after a check that
	// none was running: a turn starting in that gap appends a tool call
	// whose result is not there yet, and a history with a dangling tool
	// call is exactly what this guard exists to keep out of the fork.
	var evs []events.Event
	busy, err := d.turns.whileSessionIdle(id, func() error {
		var err error
		evs, err = d.Loop.Store.Events(id, 0)
		return err
	})
	if busy {
		http.Error(w, "a turn is in progress; cancel or wait for it before forking", http.StatusConflict)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// ParentID is deliberately left empty rather than pointing at the
	// source. It marks a background task's session, and anything with one
	// is filtered out of ListVisible — a fork is a top-level conversation
	// and has to appear in the list.
	newID := fmt.Sprintf("s-%d", time.Now().UnixNano())
	sess, err := d.Loop.Store.CreateSessionIn(newID, "", src.Agent, src.Workspace, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	for _, ev := range evs {
		// session.renamed describes the *source's* title, which the fork
		// does not share — copying it would put a claim in the fork's log
		// that it had been renamed to the original's name, and make every
		// client reload the session list on replay for nothing. It is
		// metadata about a session, not part of the conversation.
		if ev.Type == events.TypeSessionRenamed {
			continue
		}
		// Seq is reassigned by Append; the fork's log is its own sequence
		// starting at 1, which is what its clients' resume logic expects.
		if _, err := d.Loop.Store.Append(newID, ev.Type, ev.Data); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("copy event log: %w", err))
			return
		}
	}

	if title := forkTitle(src); title != "" {
		if updated, err := d.Loop.Store.SetTitle(newID, title); err == nil {
			sess = updated
		}
	}

	// Without this the fork would show the whole conversation in its
	// transcript while the model had never heard any of it — the exact
	// split-brain a restart used to cause before rehydration existed.
	d.Loop.RehydrateSession(newID)

	writeJSON(w, http.StatusCreated, sess)
}

// forkTitle names the copy after its source without stacking a prefix per
// fork, so forking a fork stays readable instead of growing
// "fork of fork of fork of ...".
func forkTitle(src *session.Session) string {
	base := src.Title
	if base == "" {
		base = src.ID
	}
	if strings.HasPrefix(base, "fork of ") {
		return base
	}
	return "fork of " + base
}

// handleListSessions returns every top-level (visible) session, newest
// first, so a client can offer "resume an existing session" instead of
// always starting a new one.
// handleListSessions lists the visible sessions, each decorated with
// whether a turn is running in it.
//
// Decorated here rather than stored on the session, because "is it
// working right now" is a fact about this process and not about the
// conversation — it must not be persisted, and it must be right on a
// fresh page load, which is what a client cannot get from the live
// activity events alone.
func (d *Daemon) handleListSessions(w http.ResponseWriter, r *http.Request) {
	busy := map[string]bool{}
	for _, id := range d.turns.running() {
		busy[id] = true
	}
	type listed struct {
		session.Session
		Busy bool `json:"busy"`
	}
	sessions := d.Loop.Store.ListVisible()
	out := make([]listed, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, listed{Session: s, Busy: busy[s.ID]})
	}
	writeJSON(w, http.StatusOK, out)
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

	// Deleting under the lock, rather than after a check: a turn that
	// began in between would be writing to a session whose file has just
	// been closed and removed.
	busy, err := d.turns.whileSessionIdle(id, func() error {
		return d.Loop.Store.Delete(id)
	})
	if busy {
		writeError(w, http.StatusConflict, fmt.Errorf("session %s has a turn in progress", id))
		return
	}
	if err != nil {
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
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
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
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
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
		// ResolveProfile, not a direct Profiles lookup: it is what the turn
		// itself calls, so it also applies the default_profile fallback for
		// an agent whose profile key is missing or unknown. Looking the map
		// up directly reported no model at all for those agents, while the
		// turn went ahead and answered with the default profile's.
		if profile, err := d.Loop.Config.ResolveProfile(name); err == nil {
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
