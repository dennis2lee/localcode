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
