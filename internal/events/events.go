// Package events defines the append-only event log that both the TUI and
// (later) a web client consume to render a session and stay in sync.
package events

import "time"

type Type string

const (
	TypeUserMessage        Type = "message.user"
	TypeMessagePartDelta   Type = "message.part.delta"
	TypeMessagePartEnd     Type = "message.part.end"
	TypeToolStart          Type = "tool.start"
	TypeToolEnd            Type = "tool.end"
	TypePermissionRequest  Type = "permission.request"
	TypePermissionResolved Type = "permission.resolved"
	TypeTaskSpawned        Type = "task.spawned"
	TypeTaskStatus         Type = "task.status"
	TypeError              Type = "error"
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
