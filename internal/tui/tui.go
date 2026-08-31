// Package tui implements a Bubble Tea front-end that talks to the core
// daemon over HTTP + SSE via internal/client — it is a client like any
// other (a Web UI is the other one), holding no conversation state beyond
// what's needed to render the current screen.
package tui

import (
	"context"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"localcode/internal/client"
	"localcode/internal/events"
	"localcode/internal/session"
)

var (
	// The user's own turn, marked at its left edge rather than in the
	// colour of the text.
	//
	// It used to be a bold cyan "You: " in front of the first line and
	// nothing else, which said who was speaking and did not say how far
	// the prompt went: a ten-line paste had one marked line and nine
	// unmarked ones that looked exactly like a model reply. The gutter
	// runs the height of the block instead, which is the same thing the
	// window does with a border down the left of .msg-user.
	//
	// The text itself is left unstyled, also matching the window. A
	// prompt is the longest thing on screen after the reply, and tinting
	// a paragraph of prose reads worse than leaving it alone.
	userStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.Border{Left: "▌"}).
			BorderLeft(true).
			BorderLeftForeground(lipgloss.Color("6")).
			PaddingLeft(1)
	toolStyle = lipgloss.NewStyle().Faint(true)
	// A prompt already on screen but not yet confirmed by the daemon: the
	// same shape in a quieter voice, so it does not move or change shape
	// when the real one replaces it.
	pendingStyle = lipgloss.NewStyle().
			Faint(true).
			BorderStyle(lipgloss.Border{Left: "▌"}).
			BorderLeft(true).
			BorderLeftForeground(lipgloss.Color("8")).
			PaddingLeft(1)
	errorStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	modalStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	statusStyle = lipgloss.NewStyle().Faint(true)
)

// inputMaxHeight caps how tall the prompt box can grow (in rows) before it
// starts scrolling internally, so a very long paste can't push the
// transcript viewport down to nothing.
const inputMaxHeight = 10

// footerLines is how many rows View() reserves below the prompt input box
// for the current-agent status line, so resizeLayout can size the viewport
// to fit.
const footerLines = 1

// borderLines is how many rows View() reserves for the top/bottom border
// drawn around the prompt input box.
const borderLines = 2

type Model struct {
	client    *client.Client
	sessionID string

	viewport   viewport.Model
	input      textarea.Model
	termHeight int
	termWidth  int
	events     <-chan events.Event
	// streamCancel stops the event stream this model is currently
	// reading, and streamGen identifies it.
	//
	// Both exist because a session can now be changed from inside the
	// program. The old stream has to end or its goroutine reads a
	// conversation nobody is looking at forever; and an event already in
	// flight when the switch happens has to be dropped, or the first
	// thing a newly opened session shows is the tail of the one before
	// it. The generation is what makes that decidable: a message
	// carries the generation of the stream it came from, and anything
	// that does not match the current one is from a session this client
	// has left.
	streamCancel context.CancelFunc
	streamGen    uint64

	// transcript holds every line ever shown, as structured entries rather
	// than one flat pre-rendered string — styling lives in view.go's
	// renderTranscript, not baked in at append time, and refreshViewport can
	// tell whether the viewport was scrolled before re-rendering. A plain
	// slice, deliberately not built on strings.Builder: Model.Update has a
	// value receiver (bubbletea's Program stores/passes the model by value
	// between calls), and strings.Builder embeds a self-referential pointer
	// it uses to detect copies — once non-empty, copying the containing
	// struct and then writing to the copy panics with "illegal use of
	// non-zero Builder copied by value". That's exactly what repeatedly
	// pressing Tab (or any rapid sequence of events) used to trigger before
	// this field existed. A slice's backing array has no such restriction.
	transcript []transcriptEntry
	// streamOpen is true between the first message.part.delta of a model
	// message and its message.part.end — while true, the next delta
	// extends the last transcript entry instead of starting a new one, so a
	// reply that streams in dozens of small chunks becomes one entry, not
	// dozens.
	streamOpen bool
	// transcriptRev counts writes to transcript. applyEvent compares it
	// across an event to decide whether to re-render: most events
	// (tool.start, task.status, agent.switched, usage) only move the status
	// line, and re-wrapping the whole transcript for each of those is both
	// wasted work on a long session and a needless chance to disturb the
	// scroll position.
	transcriptRev    uint64
	pending          *pendingPermission
	pendingHintShown bool      // has the "resolve the permission above" hint already fired for this pending request
	pendingSince     time.Time // when the current request appeared; see canAnswerPermission
	waiting          bool
	queue            []string
	errMsg           string
	currentAgent     string
	agents           []client.AgentInfo
	commandsList     []client.CommandInfo
	skillsList       []client.SkillInfo
	// refNames is the conversations "#" can complete to: the visible ones
	// and the archived ones together, since referring to a conversation is
	// reading and archiving only ever refuses starting work.
	//
	// Cached rather than fetched on the keystroke, because the key has to
	// answer instantly and this client cannot block in a key handler. A
	// stale cache costs a completion that is not offered; it cannot cost a
	// wrong reference, because the name is resolved by the daemon against
	// the real list when the message is sent.
	refNames  []session.Session
	slashList []client.SlashCommandInfo

	// picker is the open selection list, or nil. See picker.go.
	picker *picker
	// completion carries the state of a "/"-prefix completion walk, so
	// pressing the same key again offers the next candidate rather than
	// the same one. See complete.go.
	completion completionState

	// history is every prompt submitted from this client, oldest first,
	// for Up/Down recall. It's deliberately client-side and in-memory:
	// it's a typing convenience, not session state, so it neither belongs
	// in the event log nor should follow a session to another client.
	history []string
	// historyIdx is the position Up/Down navigation is currently at.
	// len(history) means "not navigating" — sitting on the entry being
	// composed rather than on a recalled one.
	historyIdx int
	// draft parks whatever was typed but not sent when history navigation
	// started, so walking back down past the newest entry returns it
	// instead of losing it.
	draft string

	// runningTool is the tool currently executing, shown in the busy
	// indicator below the prompt box. Tool activity is deliberately NOT
	// written into the transcript anymore — the indicator is its home.
	runningTool string
	// thinking is true while the model is reasoning rather than
	// answering. The busy indicator's word, not a transcript entry: it is
	// worth knowing about live and not worth scrolling past afterwards,
	// and nothing replays it.
	thinking bool
	// spin/spinning drive the indicator's animation. spinning guards
	// against starting a second tick loop: one loop keeps rescheduling
	// itself while the client is busy and dies on its first tick after
	// that clears, so only an idle->busy transition may start one.
	spin     int
	spinning bool

	// tasks tracks background tasks by ID, built from task.spawned and
	// task.status events (replayed from the start of the session on
	// connect, so the map is correct after a reattach too). It feeds the
	// busy indicator's task count and the /tasks command.
	tasks map[string]taskState
}

func New(c *client.Client, sessionID, agentName string, eventCh <-chan events.Event) Model {
	ta := textarea.New()
	ta.Placeholder = "Type a message (Enter to send, /help for help, exit to quit)"
	ta.ShowLineNumbers = false
	ta.MaxHeight = inputMaxHeight
	ta.SetHeight(1)
	// Enter sends the message (handled explicitly below); only ctrl+j
	// inserts a literal newline. Most terminals don't reliably deliver a
	// distinct shift+enter sequence, so we bind to something that works
	// everywhere instead.
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j"))
	// Drive the *real* terminal cursor rather than the textarea's own
	// drawn-in-reverse-video one. This is what makes IME composition (a
	// half-typed Hangul syllable, kana, pinyin) appear inside the prompt
	// box: the terminal paints in-progress "marked text" wherever the
	// physical cursor sits, and with a virtual cursor the physical one is
	// left parked wherever the frame happened to end. See View().
	ta.SetVirtualCursor(false)
	ta.Focus()

	vp := viewport.New(viewport.WithWidth(80), viewport.WithHeight(20))

	return Model{
		client:       c,
		sessionID:    sessionID,
		viewport:     vp,
		input:        ta,
		events:       eventCh,
		currentAgent: agentName,
		tasks:        map[string]taskState{},
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(listenForEvent(m.events, m.streamGen), m.fetchAgents(), m.fetchCommands(), m.fetchSkills(), m.fetchSlashCommands(), m.fetchReferenceNames())
}
