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

type slashCommandsMsg struct {
	commands []client.SlashCommandInfo
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
	// showThinking is the daemon's show_thinking read beside the switch,
	// nil when it could not be read. Carried here so it is in force
	// before the new stream's first event is.
	showThinking *bool
}

// settingsMsg is GET /api/settings, read when the TUI starts.
type settingsMsg struct {
	settings client.Settings
	err      error
}

// lostTurnDueMsg is a lost-turn check coming due. The first one, after a
// grace for the backlog the reconnect brought, asks the daemon whether
// the session is busy; the confirm one, after a second grace, declares
// the turn lost if nothing has ended it meanwhile. Stale unless the
// stream, the session and the epoch are the ones it was scheduled for.
type lostTurnDueMsg struct {
	sessionID string
	gen       uint64
	epoch     uint64
	confirm   bool
}

// turnCheckMsg is the daemon's answer: whether the session is running a
// turn, and whether it knows the session at all.
type turnCheckMsg struct {
	sessionID string
	gen       uint64
	epoch     uint64
	busy      bool
	found     bool
	err       error
}

type spinTickMsg struct{}

// The archive. archivedSessionsMsg is what "/retrieve" with no argument
// builds its picker from; landingSessionsMsg is deliberately not
// sessionsMsg, which opens the switch-to picker instead of switching.
// referenceNamesMsg refreshes the names "#" completes to. Distinct from
// sessionsMsg, which means "open the switch-to picker": this one changes
// nothing on screen.
type referenceNamesMsg struct {
	sessions []session.Session
	err      error
}

// effortMsg is the reasoning level for the open conversation, either
// read or just set. pick asks the picker to open on it, which is what
// "/effort-set" with no argument does.
type effortMsg struct {
	view client.EffortView
	pick bool
	err  error
}

type archivedSessionsMsg struct {
	sessions []session.Session
	err      error
}

type sessionArchivedMsg struct {
	id  string
	err error
}

type sessionRetrievedMsg struct {
	id  string
	err error
}

type sessionCreatedMsg struct {
	id  string
	err error
}

// sessionRenamedMsg and sessionDeletedMsg carry back what "/rename" and
// "/delete" did. Separate from sessionArchivedMsg because the three end
// differently: a rename leaves you where you are, an archive and a delete
// both take the conversation off the list and need somewhere to land.
type sessionRenamedMsg struct {
	title string
	err   error
}

type sessionDeletedMsg struct {
	id  string
	err error
}

// modelViewMsg is which model this conversation answers on, and whether
// the answer should open the picker.
type modelViewMsg struct {
	view client.ModelView
	pick bool
	err  error
}

// detachedMsg is the answer to Ctrl+B: which sub-agent was let go, or
// that there was none blocking this turn.
type detachedMsg struct {
	taskID   string
	detached bool
	err      error
}

type landingSessionsMsg struct {
	sessions []session.Session
	err      error
}
