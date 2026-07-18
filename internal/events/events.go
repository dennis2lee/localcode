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
	TypeAgentSwitched      Type = "agent.switched"
	TypeError              Type = "error"

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
