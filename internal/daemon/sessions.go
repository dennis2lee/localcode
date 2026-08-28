package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"localcode/internal/agent"
	"localcode/internal/events"
	"localcode/internal/hooks"
	"localcode/internal/session"
)

// admitTopLevel is the one door every top-level admission goes through:
// starting a turn, creating a conversation, forking one. It decides and
// registers in a single step, so delete-all cannot slip between the
// decision and the commit.
//
// One helper rather than the same flag read in each endpoint, because the
// endpoints are where this gets forgotten. Fork is the proof: it creates a
// user-visible conversation and had no check at all.
//
// Returns a release the caller must call, and false when the request was
// already answered with a 409.
func (d *Daemon) admitTopLevel(w http.ResponseWriter) (func(), bool) {
	release, ok := d.Loop.AdmitTopLevel()
	if !ok {
		writeError(w, http.StatusConflict, fmt.Errorf("every session is being deleted; try again in a moment"))
		return nil, false
	}
	// A hook for the tests that force the gap this window closes: the
	// moment after admission succeeds and before the thing being admitted
	// has committed. Nil in every build not running those tests.
	if topAdmitBarrier != nil {
		topAdmitBarrier()
	}
	return release, true
}

// topAdmitBarrier is test-only. See admitTopLevel.
var topAdmitBarrier func()

// forkSnapshotBarrier is test-only. It fires inside a fork's admission
// window, after the source snapshot has been read and before the
// destination is created. See handleForkSession.
var forkSnapshotBarrier func()

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
	// Delete-all is removing every session there is. Held open across the
	// creation rather than checked before it, so a delete cannot claim the
	// daemon in between and hand back an id it is about to remove.
	release, ok := d.admitTopLevel(w)
	if !ok {
		return
	}
	defer release()

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
		hooks.Run(r.Context(), d.Loop.Config.Hooks, hooks.EventSessionStart, sess.Workspace, map[string]any{
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

	// A fork is a top-level conversation like any other, so it goes
	// through the same door — and it goes through *before* reading the
	// source, not just before creating the destination. The fork depends
	// on state: a window opened after the snapshot would let delete-all
	// run to completion against the source and then watch a conversation
	// reappear, copied from a log that no longer exists. Admission-first
	// gives the whole fork one linearization point: either it reads the
	// source inside the window and delete-all waits (and then removes the
	// fork too), or delete-all wins and the fork is refused before it has
	// read anything.
	release, ok := d.admitTopLevel(w)
	if !ok {
		return
	}
	defer release()

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

	// A hook for the test that forces the state-dependence gap: the
	// snapshot is captured, the destination does not exist yet. Nil in
	// every build not running that test.
	if forkSnapshotBarrier != nil {
		forkSnapshotBarrier()
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

	// The first line of the fork's transcript says what it is.
	//
	// A fork is a verbatim copy, so the two conversations read identically
	// and neither says which is which — "is 'fork of X' the copy or the
	// original?" is a fair question with nothing on screen to answer it.
	// It goes in ahead of the copied events so it reads as a heading, and
	// rehydration ignores it, so the model is not told it is a copy.
	sourceName := src.Title
	if sourceName == "" {
		sourceName = src.ID
	}
	if _, err := d.Loop.Store.Append(newID, events.TypeSessionForked, map[string]any{
		"from":       src.ID,
		"from_title": sourceName,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("record the fork: %w", err))
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

// handleDeleteSession removes a session, everything it launched, and all
// of their persisted logs.
//
// The contract is the whole tree, and it is the tree because that is what
// the user is pointing at. A background task runs in a session of its
// own, invisible and unlisted, and deleting only the conversation left
// every one of those behind: still running, still able to edit files, and
// still on disk, one log per task per delete, unreachable through any
// list. "Delete this conversation" now means the conversation and the work
// it started.
//
// The order matters and is the reverse of what it was. Stop first, wait
// for it, and only then remove the records, so nothing is still writing
// to a log that has just been closed.
//
// Two guards, because they cover different things. Claiming the session
// as busy refuses a delete while a turn is in flight, and stops one from
// starting during the delete; that covers the visible session's own turn
// and any synchronous delegation underneath it, which runs inside that
// same turn. StopSessionTree covers the background children, which outlive
// turns by design and which no turn registry knows about.
//
// The session is claimed rather than held under the tracker's mutex,
// because stopping a background task means waiting for a model turn to
// unwind. Holding that mutex for the length of a provider call would
// freeze every other session's turn on the daemon.
func (d *Daemon) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Nothing is going to run under this cancel func; it is here because
	// claiming the session is what the tracker's turn slot means, and a
	// claim nobody can cancel would strand the session if this handler
	// died. The deferred end releases it either way.
	_, cancel := context.WithCancel(r.Context())
	defer cancel()
	if !d.turns.begin(id, cancel) {
		writeError(w, http.StatusConflict, fmt.Errorf("session %s has a turn in progress", id))
		return
	}
	defer d.turns.end(id)

	// Read before the delete, because after it there is nothing to read.
	// A background task's session is a child, and its parent's log is
	// where the row in the task panel comes from.
	parentID := ""
	if sess, err := d.Loop.Store.Get(id); err == nil {
		parentID = sess.ParentID
	}

	// Claim the tree, stop the work, wait for it, and only then remove
	// what it was writing to. The claim is released last, after the
	// records are gone: releasing it between the last task stopping and
	// the sessions being deleted would reopen admission into a tree that
	// is half deleted.
	ids, release := d.Loop.StopSessionTree(id)
	defer release()

	if err := d.Loop.Store.DeleteTree(id); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	// Tell the parent its task is gone.
	//
	// The panel's rows are built from the parent's own task.spawned and
	// task.status events, which is what makes them survive a reload. So
	// deleting the child alone removes the conversation and leaves the
	// row: it would come back on the next replay, pointing at a session
	// that no longer exists. Recording the removal where the row comes
	// from is what makes it stay gone.
	//
	// A status rather than a new event type, because every client already
	// reads task.status and an unknown status is ignored by one that has
	// not learned this one.
	if parentID != "" {
		d.Loop.Store.Append(parentID, events.TypeTaskStatus, map[string]any{
			"task_id": id,
			"status":  "deleted",
		})
	}
	// A child session can have unanswered permission prompts of its own.
	for _, sid := range ids {
		d.Broker.ForgetSession(sid)
	}
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
	// The barrier goes up before the busy check, not after.
	//
	// It used to be the other way round: check nothing is busy, release
	// the tracker lock, then spend however long it takes to stop the
	// background work, then delete. A message arriving in that interval
	// started a turn the check had already passed, and DeleteAll closed
	// its log while it was still writing to it. Claiming first means the
	// check answers a question that stays answered: nothing new can start
	// anywhere until this returns.
	release := d.Loop.StopEverything(nil)
	defer release()

	// A hook for the test that pins the ordering above: it runs at the
	// moment the busy check is evaluated and asserts the barrier is
	// already up. Nil in every build that is not running that test.
	if deleteAllProbe != nil {
		deleteAllProbe()
	}

	if busyIDs := d.turns.anyBusy(ids); len(busyIDs) > 0 {
		writeError(w, http.StatusConflict, fmt.Errorf("sessions with a turn in progress: %v", busyIDs))
		return
	}

	// Re-read under the claim rather than trusting the snapshot taken
	// before it: a session created between the two is one DeleteAll would
	// remove and this cleanup would miss.
	current := d.Loop.Store.AllSessions()
	currentIDs := make([]string, len(current))
	for i, s := range current {
		currentIDs[i] = s.ID
	}
	if d.Tasks != nil {
		d.Tasks.StopSession(currentIDs)
	}
	for _, sid := range currentIDs {
		d.Loop.ClearSessionState(sid)
	}

	if err := d.Loop.Store.DeleteAll(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	for _, sid := range currentIDs {
		d.Broker.ForgetSession(sid)
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteAllProbe is test-only. See handleDeleteAllSessions.
var deleteAllProbe func()

// handleReorderSessions records the order the session panel was dragged
// into. Cosmetic, and persisted with the rest of a session's metadata, so
// an arrangement survives a restart — an order that had to be redone every
// time the daemon came back would not be worth making.
func (d *Daemon) handleReorderSessions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(jsonBody(w, r)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := d.Loop.Store.SetOrder(req.IDs); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
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
	_, appendErr := d.Loop.Store.Append(id, events.TypeAgentSwitched, map[string]any{"agent": req.Agent})
	logAppend(id, events.TypeAgentSwitched, appendErr)

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
	_, appendErr := d.Loop.Store.Append(id, events.TypeSessionRenamed, map[string]any{"title": req.Title})
	logAppend(id, events.TypeSessionRenamed, appendErr)

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

// handleListSlashCommands returns the commands the daemon answers
// itself, so a client can complete one without a hardcoded copy of the
// list. The clients already fetch skills and custom commands; this is
// the third kind, and the only one they had no way to ask about.
func (d *Daemon) handleListSlashCommands(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, agent.SlashCommands())
}

// SkillInfo is the client-facing view of an installed skill.
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// handleListSkills returns every installed skill, name and description
// only.
//
// It exists because both clients had to be told what a skill is called
// before they could offer to complete one, and neither had any way to
// ask: "/skill" is answered on the daemon and its listing arrives as
// transcript text, which is a thing to read rather than a thing to
// complete against. The body is what a skill is called and what it is
// for, never a skill's body, which can be long and is not a listing.
func (d *Daemon) handleListSkills(w http.ResponseWriter, r *http.Request) {
	list := d.Loop.SkillList()
	out := make([]SkillInfo, 0, len(list))
	for _, s := range list {
		out = append(out, SkillInfo{Name: s.Name, Description: s.Description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
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
