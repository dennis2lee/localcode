package tui

import (
	"localcode/internal/client"
	"localcode/internal/events"
	"localcode/internal/session"
)

// eventMsg is one event off the stream, with the generation of the
// stream that produced it. See listenForEvent.
type eventMsg struct {
	ev  events.Event
	gen uint64
}

// streamEndedMsg says a stream's channel closed. For the current
// generation that is the daemon going away; for an older one it is the
// expected end of a stream this client switched off.
type streamEndedMsg struct {
	gen uint64
}

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

type taskCancelledMsg struct {
	taskID string
	err    error
}

type commandsMsg struct {
	commands []client.CommandInfo
	err      error
}

type skillsMsg struct {
	skills []client.SkillInfo
	err    error
}

type sessionsMsg struct {
	sessions []session.Session
	err      error
}

// sessionSwitchedMsg carries the outcome of opening another session: the
// new stream to read, or the reason there is none.
type sessionSwitchedMsg struct {
	sessionID string
	agent     string
	events    <-chan events.Event
	cancel    func()
	gen       uint64
	err       error
	// reattach marks a re-open of the session already on screen, after a
	// failed switch left this client attached to nothing. The transcript
	// is not rebuilt for one.
	reattach bool
}

type spinTickMsg struct{}
