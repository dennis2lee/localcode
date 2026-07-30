package tui

import (
	"localcode/internal/client"
	"localcode/internal/events"
)

type eventMsg events.Event

type turnDoneMsg struct {
	text string
	err  error
}

// opErrMsg reports the outcome of a fire-and-forget daemon call whose
// actual state change is observed through an event instead of the reply
// itself — permission resolution, turn cancellation, and an agent switch
// all update Model via the event that call causes the daemon to
// broadcast, so only a failure needs reporting here. Collapses what used
// to be three byte-identical message types (one per call).
type opErrMsg struct {
	op  string
	err error
}

type versionMsg struct {
	version string
	err     error
}

type agentsMsg struct {
	agents []client.AgentInfo
	err    error
}

type taskOutputMsg struct {
	taskID string
	output string
	err    error
}

type commandsMsg struct {
	commands []client.CommandInfo
	err      error
}

type spinTickMsg struct{}
