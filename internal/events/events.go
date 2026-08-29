// Package events defines the append-only event log that both the TUI and
// (later) a web client consume to render a session and stay in sync.
package events

import "time"

type Type string

const (
	// TypeUserMessage records what the user typed: {"text",
	// "model_text","local"}. "model_text", if present, is what the model
	// actually received when it differs from the displayed "text" (e.g.
	// "/skill foo" expands to that skill's full body). "local": true
	// marks a message answered without any model call (/usage, /compact,
	// /config, /memory, a blocked/unknown command, ...) — its paired
	// reply is a display-only echo, not something the model ever said,
	// and both are skipped when reconstructing model history from the
	// log (see agent.rehydrateHistory).
	TypeUserMessage        Type = "message.user"
	TypeMessagePartDelta   Type = "message.part.delta"
	TypeMessagePartEnd     Type = "message.part.end"
	TypeToolStart          Type = "tool.start"
	TypeToolEnd            Type = "tool.end"
	TypePermissionRequest  Type = "permission.request"
	TypePermissionResolved Type = "permission.resolved"
	TypeTaskSpawned        Type = "task.spawned"
	TypeTaskStatus         Type = "task.status"
	// TypeTaskProgress reports what a background task is doing right now,
	// mirrored into the parent conversation: {"task_id", "doing"}.
	//
	// Transient (broadcast, never written to a log): it is true only at
	// the moment it is sent, and a line per tool call would bury the
	// conversation it is mirrored into. A status of "running" for twenty
	// minutes says nothing about whether anything is happening; the name
	// of the tool the task is in says a great deal.
	TypeTaskProgress  Type = "task.progress"
	TypeAgentSwitched Type = "agent.switched"

	// The model's own reasoning, while it is happening: {"text"} on each
	// delta, and an empty payload when a block ends.
	//
	// Broadcast, never written to a log, and that is the whole of the
	// design. Reasoning is worth watching live and is not part of the
	// conversation afterwards — the API does not want it back on a later
	// turn, and a transcript that keeps it makes every reply twice as
	// long to scroll past for something nobody reads twice. A client that
	// is not looking simply misses it, which is correct.
	TypeThinkingDelta Type = "thinking.delta"
	TypeThinkingEnd   Type = "thinking.end"
	TypeError         Type = "error"

	// TypeMCPStatus reports the state of every configured MCP server:
	// {"servers":[{"name","status","detail"}]}, status being one of
	// connected/degraded/disconnected. Unlike every other type here it is
	// daemon-wide rather than per-conversation, and is never written to a
	// session's event log — it describes the present moment, so replaying
	// an hour-old sequence of it to a reconnecting client would be worse
	// than useless. It is fanned out live (see daemon.broadcaster) and
	// carries the complete list every time, so a client that missed one
	// is corrected by the next.
	TypeMCPStatus Type = "mcp.status"

	// TypeSessionActivity reports that one session started or stopped
	// working. Daemon-wide, not part of any session's log: it is a fact
	// about right now, and replaying an hour of light changes to a
	// reconnecting client would be worse than useless. Clients showing a
	// per-session indicator read the current state from the session list
	// on load and keep it current from these.
	TypeSessionActivity Type = "session.activity"

	// TypeSettingsChanged reports the daemon-wide switches after one of
	// them moved, whoever moved it: a toggle typed at a prompt, the
	// settings window, another client entirely.
	//
	// Daemon-wide, so it goes out on the broadcast rather than into a
	// session's log. A setting is not part of any conversation, and the
	// session-scoped TypeConfigChanged could only reach clients that
	// happened to be looking at the session the command was typed in,
	// which is why a second window went on showing the old state.
	//
	// It carries every switch rather than the one that moved, so a
	// client applies a snapshot instead of merging a sequence and cannot
	// end up half-updated by a missed event. Fan-out only, no history: a
	// client that was not connected re-reads GET /api/settings on load,
	// which is where it gets these in the first place.
	TypeSettingsChanged Type = "settings.changed"

	// TypeUsage reports the latest known token usage/context-window fill
	// for a turn: {"input_tokens","output_tokens","max_context","percent",
	// "tps","show_tps","model"}.
	TypeUsage Type = "usage"
	// TypeCompacted marks that compaction replaced a session's in-memory
	// history with a summary: {"summary_length","manual","summary",
	// "model","input_tokens","output_tokens"} (the last three are omitted
	// if the compaction call didn't report usage). "summary" carries the
	// full text (not just its length) so a restart can restore the exact
	// post-compaction history — see agent.rehydrateHistory.
	TypeCompacted Type = "compacted"
	// TypeConfigChanged reports a live settings change from "/config":
	// {"auto_compact_enabled","show_tps"}.
	TypeConfigChanged Type = "config.changed"
	// The scheduled-task events. Written to the conversation a task was
	// booked in, which is what makes its row in the panel survive a
	// reload — the same reason a background task's row is built from
	// task.spawned rather than from whatever the client happens to hold.
	//
	// TypeScheduleCreated: {"id","at","prompt","agent"}.
	// TypeScheduleStatus: {"id","status","run_session","error"}.
	// TypeScheduleSeen: {"id"} — somebody read the result, which is the
	// third state of the row's light.
	// TypeScheduleRenamed: {"id","name"} — cosmetic, like a session's
	// title. An empty name clears it and the row goes back to showing the
	// prompt.
	// TypeScheduleRemoved: {"id"}.
	TypeScheduleCreated Type = "schedule.created"
	TypeScheduleStatus  Type = "schedule.status"
	TypeScheduleSeen    Type = "schedule.seen"
	TypeScheduleRenamed Type = "schedule.renamed"
	TypeScheduleRemoved Type = "schedule.removed"

	// The debate events: one agent doing the work, another reviewing it,
	// round after round, with localcode counting.
	//
	// TypeDebateStarted: {"author","reviewer","model","rounds","task"} —
	// written before the first turn, so a client can say what is about to
	// happen rather than explaining it afterwards.
	// TypeDebateReview: {"round","rounds","reviewer","model","text",
	// "approved","session"}. The review itself, carried here rather than
	// as an ordinary message because it is neither the person speaking
	// nor this session's model: it is another agent's words, and a client
	// that paints it as either is lying about who said it. "session" is
	// the reviewer's own session, so the row can be opened and read.
	// TypeDebateEnded: {"reason","rounds","approved","note"}, reason being
	// one of approved / rounds / stalled / stopped / failed. "note" is the
	// sentence to show; it rides on the event rather than being written as
	// a reply, because a reply with no user message before it rehydrates
	// as a second assistant message in a row.
	TypeDebateStarted Type = "debate.started"
	TypeDebateReview  Type = "debate.review"
	TypeDebateEnded   Type = "debate.ended"

	// TypePermissionsChanged reports this session's four permission
	// switches after one of them moved, along with the directories it
	// currently remembers approving outside the workspace.
	//
	// Its own event rather than a field on config.changed, because these
	// are per session and that one is about the daemon: a client showing
	// conversation A must not repaint its switches because conversation B
	// answered a prompt.
	TypePermissionsChanged Type = "permissions.changed"
	// TypeSessionRenamed reports a session's title changing: {"title"}.
	TypeSessionRenamed Type = "session.renamed"
	// TypeSessionForked opens the log of a session created by forking
	// another, naming what it was forked from: {"from", "from_title"}.
	//
	// A fork is a copy of a conversation, so the two transcripts are
	// identical and nothing in either one says which is which. "Is 'fork
	// of X' the copy or the original?" is then a fair question with no
	// answer on screen — this is the answer, written where it is read.
	// Purely a note for the reader: rehydration ignores it, so the model
	// is not told it is talking to a copy.
	TypeSessionForked Type = "session.forked"
	// TypeDelegated marks a turn answered by a sub-agent on its own model
	// instead of the session's own: {"agent", "prompt"}. Clients show it so
	// a cheaper model answering is visible rather than silent.
	TypeDelegated Type = "delegated"
	// TypeTurnDone marks the real end of a turn: every model message,
	// tool call, and follow-up response has finished and the daemon has
	// already cleared the session's busy flag, so a message sent on
	// seeing this event cannot hit 409. Clients gate their "waiting"
	// state and prompt-queue drain on this, not on message.part.end —
	// that one fires per model message, and a turn with tool calls has
	// several.
	TypeTurnDone Type = "turn.done"
	// TypeTurnCancelled marks a turn stopped on purpose by the user
	// (Esc in the TUI, POST /api/sessions/{id}/cancel), as opposed to
	// TypeError which means something went wrong. Clients use it to stop
	// showing a spinner without printing a scary message.
	TypeTurnCancelled Type = "turn.cancelled"
)

// Event is one entry in a session's append-only log. Seq is monotonically
// increasing per session; clients poll/subscribe with `since=<seq>`.
type Event struct {
	Seq       uint64         `json:"seq"`
	Session   string         `json:"session"`
	Type      Type           `json:"type"`
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data,omitempty"`
}
