// Package tui implements a Bubble Tea front-end that talks to the core
// daemon over HTTP + SSE via internal/client — it is a client like any
// other (a Web UI is the other one), holding no conversation state beyond
// what's needed to render the current screen.
package tui

import (
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"localcode/internal/client"
	"localcode/internal/events"
)

var (
	userStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	toolStyle = lipgloss.NewStyle().Faint(true)
	// A prompt already on screen but not yet confirmed by the daemon: the
	// user line's colour, without its weight, so it reads as the same line
	// in a quieter voice.
	pendingStyle = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("6"))
	errorStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	modalStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	statusStyle  = lipgloss.NewStyle().Faint(true)
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
	events     <-chan events.Event

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
	return tea.Batch(listenForEvent(m.events), m.fetchAgents(), m.fetchCommands())
}
