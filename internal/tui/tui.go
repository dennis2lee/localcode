// Package tui implements a Bubble Tea front-end that talks to the core
// daemon over HTTP + SSE via internal/client — it is a client like any
// other (a Web UI is the other one), holding no conversation state beyond
// what's needed to render the current screen.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
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
	userStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	toolStyle   = lipgloss.NewStyle().Faint(true)
	errorStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	modalStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	statusStyle = lipgloss.NewStyle().Faint(true)
)

// inputMaxHeight caps how tall the prompt box can grow (in rows) before it
// starts scrolling internally, so a very long paste can't push the
// transcript viewport down to nothing.
const inputMaxHeight = 10

const helpText = `Available commands:
  /help              show this help
  /version            show the daemon version
  /skill              list registered skills
  /<skill name>        run that skill (e.g. /pdf-tools)
  /agent              list registered agents
  /agent <name>        switch to that agent (Tab also cycles through them)
  /init              scan the repo and create/improve an AGENTS.md rules file
  /memory            show the auto memory directory/index (MEMORY.md)
  /config            show current settings (auto_compact, show_tps, auto_delegate)
  /config auto_compact on|off   toggle auto-compaction above 80% context usage
  /config show_tps on|off       toggle the tokens/sec display under the prompt
  /config auto_delegate on|off  send matching prompts to a cheaper sub-agent
  /compact           summarize and compact the conversation right now
  /compact <instructions>      give instructions for how to compact
  /usage              show cumulative token usage per model
  /tasks              list background tasks and their status
  /tasks <id>          show everything that task has produced so far
  /commands          list registered custom commands
  /<custom command>   run a command defined in .localcode/commands/*.md
  exit, :q            quit the TUI (same as Ctrl+C)

Enter to send, Ctrl+J for a newline, Tab to switch agents, Esc to cancel a running turn.`

// footerLines is how many rows View() reserves below the prompt input box
// for the current-agent status line, so resizeLayout can size the viewport
// to fit.
const footerLines = 1

// borderLines is how many rows View() reserves for the top/bottom border
// drawn around the prompt input box.
const borderLines = 2

type pendingPermission struct {
	id, tool, description string
	// rule is the pattern a "session" or "always" answer would grant
	// (e.g. "git *" for a bash call, or the exact path for a file tool) —
	// shown in the prompt so approving a wider scope is an informed
	// choice, not a guess.
	rule string
	// canAlways is false when the daemon has no config.json path to write
	// to (started with neither --config nor a resolvable global config),
	// in which case "always" isn't offered — only once/session/deny.
	canAlways bool
}

// prompt renders the permission modal's single line, listing exactly the
// answers this request will accept — "a" only appears when the daemon
// actually has somewhere to persist it.
func (p pendingPermission) prompt() string {
	keys := "y: allow once  n: deny  s: allow for session"
	if p.canAlways {
		keys += fmt.Sprintf("  a: always allow %q", p.rule)
	}
	return fmt.Sprintf("Permission request [%s]: %s\n%s", p.tool, p.description, keys)
}

type Model struct {
	client    *client.Client
	sessionID string

	viewport   viewport.Model
	input      textarea.Model
	termHeight int
	events     <-chan events.Event

	// transcript is a plain string, deliberately not a strings.Builder:
	// Model.Update has a value receiver (bubbletea's Program stores/passes
	// the model by value between calls), and strings.Builder embeds a
	// self-referential pointer it uses to detect copies — once non-empty,
	// copying the containing struct and then writing to the copy panics
	// with "illegal use of non-zero Builder copied by value". That's
	// exactly what repeatedly pressing Tab (or any rapid sequence of
	// events) used to trigger. Plain strings have no such restriction.
	transcript   string
	pending      *pendingPermission
	waiting      bool
	queue        []string
	errMsg       string
	currentAgent string
	agents       []client.AgentInfo
	commandsList []client.CommandInfo

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

// taskState is what the TUI knows about one background task, entirely
// from the parent session's events.
type taskState struct {
	agent  string
	status string // spawned | running | completed | failed | cancelled
	prompt string
}

// active reports whether the task is still doing work.
func (t taskState) active() bool { return t.status == "spawned" || t.status == "running" }

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

type eventMsg events.Event
type turnDoneMsg struct {
	text string
	err  error
}
type permissionResolvedMsg struct{ err error }
type versionMsg struct {
	version string
	err     error
}
type agentsMsg struct {
	agents []client.AgentInfo
	err    error
}
type switchAgentMsg struct{ err error }
type turnCancelledMsg struct{ err error }
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

// spinFrames is the busy indicator's animation. Braille spinners render
// in every terminal the TUI targets (Windows Terminal included).
var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func spinTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return spinTickMsg{} })
}

// startSpin begins the indicator's tick loop, unless one is already
// running — a second loop would double the animation speed and never
// stop cleanly.
func (m *Model) startSpin() tea.Cmd {
	if m.spinning {
		return nil
	}
	m.spinning = true
	return spinTick()
}

// activeTasks counts background tasks still doing work.
func (m Model) activeTasks() int {
	n := 0
	for _, t := range m.tasks {
		if t.active() {
			n++
		}
	}
	return n
}

// busy reports whether anything is running that the indicator should
// show: this client's own turn, or background tasks.
func (m Model) busy() bool { return m.waiting || m.activeTasks() > 0 }

// busyLine renders the indicator shown below the prompt box while
// anything is running: an animation frame, what the turn is doing (the
// running tool's name when one is executing), the queue depth, and the
// background-task count. It replaces the old per-event "[tool] ..."
// transcript lines entirely.
func (m Model) busyLine() string {
	frame := spinFrames[m.spin%len(spinFrames)]
	var parts []string
	if m.waiting {
		what := "working"
		if m.runningTool != "" {
			what = m.runningTool
		}
		part := what + "… esc to cancel"
		if n := len(m.queue); n > 0 {
			part += fmt.Sprintf(" (%d queued)", n)
		}
		parts = append(parts, part)
	}
	if n := m.activeTasks(); n > 0 {
		noun := "background task"
		if n > 1 {
			noun += "s"
		}
		parts = append(parts, fmt.Sprintf("%d %s (/tasks to inspect)", n, noun))
	}
	return frame + " " + strings.Join(parts, "  ·  ")
}

// tasksSummary renders the /tasks listing from the state built out of
// task.spawned/task.status events — no server round trip needed for the
// list itself; /tasks <id> fetches that task's output.
func (m Model) tasksSummary() string {
	if len(m.tasks) == 0 {
		return "No background tasks in this session."
	}
	ids := make([]string, 0, len(m.tasks))
	for id := range m.tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	b.WriteString("Background tasks (/tasks <id> for its output so far):\n")
	for _, id := range ids {
		t := m.tasks[id]
		fmt.Fprintf(&b, "- %s [%s] %s: %s\n", id, t.status, t.agent, t.prompt)
	}
	return strings.TrimRight(b.String(), "\n")
}

func listenForEvent(ch <-chan events.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return eventMsg(ev)
	}
}

func (m Model) sendMessage(text string) tea.Cmd {
	return func() tea.Msg {
		err := m.client.SendMessage(context.Background(), m.sessionID, text)
		return turnDoneMsg{text: text, err: err}
	}
}

// rememberPrompt appends a submitted prompt to the recall history and
// resets navigation back to the composing position. Consecutive duplicates
// are collapsed, the way a shell's history does, so holding Enter on the
// same message doesn't bury everything else behind repeats.
func (m *Model) rememberPrompt(text string) {
	if n := len(m.history); n == 0 || m.history[n-1] != text {
		m.history = append(m.history, text)
	}
	m.historyIdx = len(m.history)
	m.draft = ""
}

// atInputTop reports whether the cursor sits on the very first visual row
// of the prompt box, which is when Up should recall history instead of
// moving the cursor. RowOffset accounts for a single long logical line
// that soft-wrapped across several rows: being on logical line 0 is not
// enough, the cursor also has to be on that line's first row.
func (m Model) atInputTop() bool {
	return m.input.Line() == 0 && m.input.LineInfo().RowOffset == 0
}

// atInputBottom is atInputTop's mirror: the last row of the last logical
// line, where Down should step forward through history.
func (m Model) atInputBottom() bool {
	info := m.input.LineInfo()
	return m.input.Line() == m.input.LineCount()-1 && info.RowOffset == info.Height-1
}

// setInputTo replaces the prompt contents and parks the cursor at the end,
// which is where you want it after recalling something to edit.
func (m *Model) setInputTo(text string) {
	m.input.SetValue(text)
	m.input.CursorEnd()
	m.resizeLayout()
}

// historyPrev recalls the previous entry. Returns false when there is
// nothing older to go to, so the caller can let the keypress fall through
// to the textarea instead of swallowing it.
func (m *Model) historyPrev() bool {
	if len(m.history) == 0 || m.historyIdx == 0 {
		return false
	}
	if m.historyIdx == len(m.history) {
		// Leaving the composing position: stash what's there to come back to.
		m.draft = m.input.Value()
	}
	m.historyIdx--
	m.setInputTo(m.history[m.historyIdx])
	return true
}

// historyNext walks back toward the newest entry, and one step past it
// restores the draft that was being composed before recall started.
func (m *Model) historyNext() bool {
	if m.historyIdx >= len(m.history) {
		return false
	}
	m.historyIdx++
	if m.historyIdx == len(m.history) {
		m.setInputTo(m.draft)
		m.draft = ""
		return true
	}
	m.setInputTo(m.history[m.historyIdx])
	return true
}

// isPlainPrompt reports whether text is an ordinary chat message rather
// than something the TUI itself intercepts (a "/"-prefixed local or
// server-side command, or exit/:q). Only plain prompts are safe to queue
// while a turn is in progress — queueing a command would mean replaying it
// as literal chat text to the model once dequeued, instead of running it.
func isPlainPrompt(text string) bool {
	lower := strings.ToLower(text)
	return !strings.HasPrefix(text, "/") && lower != "exit" && lower != ":q"
}

// dequeue sends the next queued prompt once the current turn has actually
// finished (m.waiting was just cleared) — the common case for someone who
// kept typing while the model was still streaming a reply. Returns nil if
// nothing is queued or a turn is still in progress.
func (m *Model) dequeue() tea.Cmd {
	if m.waiting || len(m.queue) == 0 {
		return nil
	}
	next := m.queue[0]
	m.queue = m.queue[1:]
	m.waiting = true
	return m.sendMessage(next)
}

// cancelTurn asks the daemon to stop the running turn. The transcript
// line and the cleared spinner come from the turn.cancelled event the
// daemon broadcasts, not from here, so every attached client reacts to a
// cancel the same way regardless of which one pressed Esc.
func (m Model) cancelTurn() tea.Cmd {
	return func() tea.Msg {
		err := m.client.CancelTurn(context.Background(), m.sessionID)
		return turnCancelledMsg{err: err}
	}
}

// resolvePermission answers a pending permission request. scope is
// "once" (or ""), "session", or "always" — see agent.PermissionBroker.
// The actual policy change (remembering the grant, writing it to
// config.json) happens server-side; this only reports what the user
// chose.
func (m Model) resolvePermission(id string, allow bool, scope string) tea.Cmd {
	return func() tea.Msg {
		err := m.client.ResolvePermission(context.Background(), m.sessionID, id, allow, scope)
		return permissionResolvedMsg{err: err}
	}
}

func (m Model) fetchVersion() tea.Cmd {
	return func() tea.Msg {
		v, err := m.client.Version(context.Background())
		return versionMsg{version: v, err: err}
	}
}

func (m Model) fetchAgents() tea.Cmd {
	return func() tea.Msg {
		agents, err := m.client.ListAgents(context.Background())
		return agentsMsg{agents: agents, err: err}
	}
}

// fetchTaskOutput pulls everything a background task has produced so far
// (works mid-run — a task is a session, so its stream is readable while
// it is still going).
func (m Model) fetchTaskOutput(taskID string) tea.Cmd {
	return func() tea.Msg {
		out, err := m.client.TaskOutput(context.Background(), taskID)
		return taskOutputMsg{taskID: taskID, output: out, err: err}
	}
}

func (m Model) fetchCommands() tea.Cmd {
	return func() tea.Msg {
		cmds, err := m.client.ListCommands(context.Background())
		return commandsMsg{commands: cmds, err: err}
	}
}

// switchAgent asks the daemon to change this session's active agent. It
// reports only errors back to Update — the actual state change (and the
// transcript line announcing it) comes from the agent.switched event this
// same call causes the daemon to broadcast, which every subscribed client
// (including this one) receives the same way.
func (m Model) switchAgent(name string) tea.Cmd {
	return func() tea.Msg {
		_, err := m.client.SwitchAgent(context.Background(), m.sessionID, name)
		return switchAgentMsg{err: err}
	}
}

// nextAgent returns the agent after currentAgent in m.agents, cycling
// back to the start — the Tab-key behavior. Returns "", false if there's
// nothing to cycle to (0 or 1 known agents).
func (m Model) nextAgent() (string, bool) {
	if len(m.agents) < 2 {
		return "", false
	}
	for i, a := range m.agents {
		if a.Name == m.currentAgent {
			return m.agents[(i+1)%len(m.agents)].Name, true
		}
	}
	// Current agent isn't in the known list (shouldn't normally happen) —
	// just start from the first one.
	return m.agents[0].Name, true
}

// currentModel returns the model ID m.currentAgent's profile resolves to
// (e.g. "us.anthropic.claude-sonnet-4-6"), for display in the footer.
// Returns "", false if the current agent isn't in the known list yet
// (e.g. GET /api/agents hasn't come back) or its profile has no model set.
func (m Model) currentModel() (string, bool) {
	for _, a := range m.agents {
		if a.Name == m.currentAgent {
			return a.Model, a.Model != ""
		}
	}
	return "", false
}

// appendLocal writes text straight into the transcript without going
// through the server — for /help and /version, which are answered purely
// client-side (well, /version does hit the daemon, but the answer isn't
// part of the session's event log either way).
func (m *Model) appendLocal(text string) {
	m.transcript += toolStyle.Render(text) + "\n\n"
	m.refreshViewport()
}

// refreshViewport pushes the current transcript into the viewport,
// word-wrapped to the viewport's width first. The viewport itself never
// wraps on its own — bubbles/viewport just renders whatever lines it's
// given and lets long ones run off the right edge — so without this, a
// model reply with no newlines in it (the common case) streams straight
// off-screen instead of becoming readable multi-line text. lipgloss's
// Width() wraps at rune boundaries while still measuring printable width
// correctly around the ANSI styling userStyle/toolStyle/etc. already
// applied to parts of the transcript.
func (m *Model) refreshViewport() {
	w := m.viewport.Width()
	if w <= 0 {
		w = 80
	}
	m.viewport.SetContent(lipgloss.NewStyle().Width(w).Render(m.transcript))
	m.viewport.GotoBottom()
}

// resizeLayout recomputes the input box height from its current content and
// gives the viewport whatever vertical space is left, so the prompt grows
// as the user types a longer message without pushing the input off screen.
func (m *Model) resizeLayout() {
	inputHeight := m.input.LineCount()
	if inputHeight > inputMaxHeight {
		inputHeight = inputMaxHeight
	}
	if inputHeight < 1 {
		inputHeight = 1
	}
	m.input.SetHeight(inputHeight)
	m.scrollInputToTop()

	const chromeLines = 2 // status/permission line + blank separator
	vh := m.termHeight - chromeLines - borderLines - footerLines - inputHeight
	if vh < 3 {
		vh = 3
	}
	m.viewport.SetHeight(vh)
}

// scrollInputToTop pulls the prompt box's internal viewport back to the
// first line when the whole value fits in the box, preserving the cursor.
//
// Without this, a multi-line paste renders as a blank black block. The
// paste arrives while the box is still one row tall, so the textarea
// scrolls down to keep the cursor visible; resizeLayout then grows the
// box, but the textarea's repositionView only ever scrolls to bring the
// cursor *into* view, never back up once everything fits. The offset
// therefore sticks, the first lines stay scrolled out of sight, and the
// rows past the end render as black-on-black filler — while Value() is
// perfectly correct, which is why sending it worked.
func (m *Model) scrollInputToTop() {
	if m.input.LineCount() > m.input.Height() {
		return // genuinely taller than the box; the offset is doing real work
	}
	row := m.input.Line()
	col := m.input.LineInfo().ColumnOffset
	m.input.MoveToBegin() // scrolls the internal viewport back to line 0
	for i := 0; i < row; i++ {
		m.input.CursorDown()
	}
	m.input.SetCursorColumn(col)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spinTickMsg:
		if !m.busy() {
			// Nothing running anymore — let this loop die. The next
			// idle->busy transition starts a fresh one.
			m.spinning = false
			return m, nil
		}
		m.spin++
		return m, spinTick()

	case tea.WindowSizeMsg:
		m.termHeight = msg.Height
		m.viewport.SetWidth(msg.Width)
		m.input.SetWidth(msg.Width - 2)
		m.resizeLayout()
		// Re-wrap the existing transcript at the new width — it was
		// wrapped for whatever width was current the last time something
		// was appended, which a resize just invalidated.
		m.refreshViewport()
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit

		case "esc":
			// Esc stops whatever is running. Queued prompts go with it:
			// the whole point of cancelling is to stop, so letting the
			// queue immediately fire the next message would be the
			// opposite of what was asked for.
			if m.waiting {
				m.queue = nil
				return m, m.cancelTurn()
			}
			return m, nil

		case "y", "n", "s", "a":
			if m.pending != nil {
				id := m.pending.id
				canAlways := m.pending.canAlways
				m.pending = nil
				switch msg.String() {
				case "n":
					return m, m.resolvePermission(id, false, "")
				case "s":
					return m, m.resolvePermission(id, true, "session")
				case "a":
					if !canAlways {
						return m, nil // no config.json to write to; "a" isn't offered
					}
					return m, m.resolvePermission(id, true, "always")
				default: // "y"
					return m, m.resolvePermission(id, true, "")
				}
			}

		case "up":
			// Recall only when the cursor can't move any further up
			// inside the box, so Up still navigates a multi-line prompt
			// normally and only reaches for history at the boundary.
			if m.pending == nil && m.atInputTop() && m.historyPrev() {
				return m, nil
			}

		case "down":
			if m.pending == nil && m.atInputBottom() && m.historyNext() {
				return m, nil
			}

		case "tab":
			if next, ok := m.nextAgent(); ok {
				return m, m.switchAgent(next)
			}
			return m, nil

		case "enter":
			if m.pending != nil {
				return m, nil // waiting for y/n, ignore stray enters
			}
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}

			// A turn is already streaming: queue a plain prompt so it sends
			// automatically the moment the current one finishes, instead of
			// silently dropping it and making the user remember to retype
			// it. Local commands and /agent still wait for the turn to
			// finish first, same as before — they don't go through
			// sendMessage, so queueing them would mean replaying them as
			// literal chat text later.
			// Only a foreground turn blocks: background tasks run in their
			// own child sessions, so the daemon's per-session busy flag is
			// clear and a new prompt can go out immediately.
			if m.waiting {
				if isPlainPrompt(text) {
					m.queue = append(m.queue, text)
					m.appendLocal(fmt.Sprintf("[queued] %s", text))
				}
				m.rememberPrompt(text)
				m.input.Reset()
				m.resizeLayout()
				return m, nil
			}

			m.rememberPrompt(text)
			m.input.Reset()
			m.resizeLayout()

			switch strings.ToLower(text) {
			case "exit", ":q":
				return m, tea.Quit
			case "/help":
				m.appendLocal(helpText)
				return m, nil
			case "/version":
				return m, m.fetchVersion()
			case "/agent":
				if len(m.agents) == 0 {
					m.appendLocal("No agents registered.")
				} else {
					var b strings.Builder
					b.WriteString("Available agents (/agent <name> to switch, current: " + m.currentAgent + "):\n")
					for _, a := range m.agents {
						fmt.Fprintf(&b, "- %s: %s\n", a.Name, a.Description)
					}
					m.appendLocal(strings.TrimRight(b.String(), "\n"))
				}
				return m, nil
			case "/commands":
				if len(m.commandsList) == 0 {
					m.appendLocal("No custom commands registered. (add one under .localcode/commands/*.md)")
				} else {
					var b strings.Builder
					b.WriteString("Available custom commands:\n")
					for _, c := range m.commandsList {
						fmt.Fprintf(&b, "- /%s: %s\n", c.Name, c.Description)
					}
					m.appendLocal(strings.TrimRight(b.String(), "\n"))
				}
				return m, nil
			}
			if name, ok := strings.CutPrefix(text, "/agent "); ok {
				name = strings.TrimSpace(name)
				return m, m.switchAgent(name)
			}
			if strings.ToLower(text) == "/tasks" {
				m.appendLocal(m.tasksSummary())
				return m, nil
			}
			if id, ok := strings.CutPrefix(text, "/tasks "); ok {
				return m, m.fetchTaskOutput(strings.TrimSpace(id))
			}

			// The user line itself renders from the message.user event (see
			// applyEvent), not optimistically here, so a resumed/replayed
			// session shows the same transcript a live one did.
			m.waiting = true
			return m, tea.Batch(m.sendMessage(text), m.startSpin())
		}

	case eventMsg:
		m.applyEvent(events.Event(msg))
		cmds := []tea.Cmd{listenForEvent(m.events)}
		if cmd := m.dequeue(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		// An event can make us busy without a keypress (a background task
		// spawned, a queued prompt just went out) — make sure the
		// indicator animates then too. startSpin is a no-op when a loop
		// is already running.
		if m.busy() {
			if cmd := m.startSpin(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		return m, tea.Batch(cmds...)

	case turnDoneMsg:
		if client.IsBusy(msg.err) {
			// The daemon already has a turn running (typed during a race
			// window, or another client's turn). That is queue material,
			// not an error: put it back at the front and wait for the
			// running turn's turn.done to drain it.
			m.queue = append([]string{msg.text}, m.queue...)
			m.waiting = true
			m.appendLocal(fmt.Sprintf("[queued] %s", msg.text))
			return m, m.startSpin()
		}
		if msg.err != nil {
			m.waiting = false
			m.errMsg = msg.err.Error()
			if cmd := m.dequeue(); cmd != nil {
				return m, cmd
			}
		}
		// On success we leave m.waiting as-is: the daemon accepted the
		// message and is streaming the actual turn via events; waiting
		// clears when a message.part.end / error event arrives.
		return m, nil

	case permissionResolvedMsg:
		if msg.err != nil {
			m.errMsg = msg.err.Error()
		}
		return m, nil

	case taskOutputMsg:
		if msg.err != nil {
			m.appendLocal(fmt.Sprintf("failed to fetch %s: %v", msg.taskID, msg.err))
			return m, nil
		}
		t := m.tasks[msg.taskID]
		header := fmt.Sprintf("--- %s [%s] %s ---", msg.taskID, t.status, t.prompt)
		out := msg.output
		if strings.TrimSpace(out) == "" {
			out = "(no output yet)"
		}
		m.appendLocal(header + "\n" + out)
		return m, nil

	case versionMsg:
		if msg.err != nil {
			m.appendLocal("failed to fetch version: " + msg.err.Error())
		} else {
			m.appendLocal("localcode " + msg.version)
		}
		return m, nil

	case agentsMsg:
		if msg.err == nil {
			m.agents = msg.agents
		}
		return m, nil

	case commandsMsg:
		if msg.err == nil {
			m.commandsList = msg.commands
		}
		return m, nil

	case turnCancelledMsg:
		if msg.err != nil {
			m.errMsg = msg.err.Error()
		}
		// m.waiting is cleared by the turn.cancelled event, the same way
		// it is for a turn that ends normally.
		return m, nil

	case switchAgentMsg:
		if msg.err != nil {
			m.errMsg = msg.err.Error()
		}
		// On success, m.currentAgent updates from the agent.switched event
		// this call causes the daemon to broadcast (see applyEvent) — not
		// here, so every client reacts to the same event uniformly.
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.resizeLayout()
	return m, cmd
}

func (m *Model) applyEvent(ev events.Event) {
	switch ev.Type {
	case events.TypeUserMessage:
		if text, ok := ev.Data["text"].(string); ok {
			m.transcript += userStyle.Render("You: ") + text + "\n\n"
		}
	case events.TypeMessagePartDelta:
		if text, ok := ev.Data["text"].(string); ok {
			m.transcript += text
		}
	case events.TypeMessagePartEnd:
		// One model message ended — NOT the turn. A turn with tool calls
		// streams several of these (text, then the post-tool follow-up),
		// and treating the first as end-of-turn is what used to make a
		// prompt typed during tool execution skip the queue and 409.
		m.transcript += "\n\n"
	case events.TypeTurnDone:
		// The daemon's real turn boundary, emitted after its busy flag is
		// cleared — safe to stop waiting and let the queue drain.
		m.waiting = false
		m.runningTool = ""
	case events.TypeToolStart:
		// No transcript line — tool activity lives in the busy indicator
		// below the prompt box, which names the running tool and vanishes
		// when the turn ends.
		m.runningTool, _ = ev.Data["name"].(string)
	case events.TypeToolEnd:
		m.runningTool = ""
	case events.TypePermissionRequest:
		id, _ := ev.Data["id"].(string)
		tool, _ := ev.Data["tool"].(string)
		desc, _ := ev.Data["description"].(string)
		rule, _ := ev.Data["rule"].(string)
		canAlways, _ := ev.Data["can_always"].(bool)
		m.pending = &pendingPermission{id: id, tool: tool, description: desc, rule: rule, canAlways: canAlways}
	case events.TypeTaskSpawned:
		// No transcript line — background tasks surface in the busy
		// indicator below the prompt box, and /tasks inspects them.
		taskID, _ := ev.Data["task_id"].(string)
		agentName, _ := ev.Data["agent"].(string)
		prompt, _ := ev.Data["prompt"].(string)
		m.tasks[taskID] = taskState{agent: agentName, status: "spawned", prompt: prompt}
	case events.TypeTaskStatus:
		taskID, _ := ev.Data["task_id"].(string)
		status, _ := ev.Data["status"].(string)
		t := m.tasks[taskID]
		t.status = status
		m.tasks[taskID] = t
	case events.TypeAgentSwitched:
		// Just update the status line the footer already renders every
		// frame — do NOT also write a transcript line here. This event
		// fires on every Tab press/switch, and appending to the
		// (persistent, ever-growing) transcript made each press leave a
		// permanent "switched to X" line on screen forever instead of
		// just updating the one-line status shown below the prompt.
		if name, ok := ev.Data["agent"].(string); ok {
			m.currentAgent = name
		}
	case events.TypeDelegated:
		if name, ok := ev.Data["agent"].(string); ok {
			m.transcript += toolStyle.Render(fmt.Sprintf("[delegated to %s]", name)) + "\n\n"
		}
	case events.TypeTurnCancelled:
		m.waiting = false
		m.runningTool = ""
		m.transcript += toolStyle.Render("[cancelled]") + "\n\n"
	case events.TypeError:
		if msg, ok := ev.Data["error"].(string); ok {
			m.waiting = false
			m.runningTool = ""
			m.errMsg = msg
		}
	}
	m.refreshViewport()
}

// inputBorder draws a horizontal rule spanning the input box's width, used
// above and below it so its boundary reads clearly against the transcript.
func (m Model) inputBorder() string {
	w := m.viewport.Width()
	if w <= 0 {
		w = 40
	}
	return statusStyle.Render(strings.Repeat("─", w))
}

// View assembles the frame as a slice of lines rather than one appended
// string, because it needs to know which row the prompt box starts on to
// place the real terminal cursor (see the tea.Cursor handoff at the end).
func (m Model) View() tea.View {
	lines := strings.Split(m.viewport.View(), "\n")

	lines = append(lines, m.inputBorder())
	// Row the prompt box's first line lands on. Derived from the frame
	// built so far rather than a hardcoded sum, so it stays correct as the
	// optional status/permission line above it comes and goes.
	inputRow := len(lines)
	lines = append(lines, strings.Split(m.input.View(), "\n")...)
	lines = append(lines, m.inputBorder())

	// The status band lives BELOW the prompt box: the permission prompt,
	// the busy indicator (own turn and/or background tasks), or the last
	// error — one at a time, gone when there is nothing to say.
	if m.pending != nil {
		lines = append(lines, modalStyle.Render(m.pending.prompt()))
	} else if m.busy() {
		lines = append(lines, statusStyle.Render(m.busyLine()))
	} else if m.errMsg != "" {
		lines = append(lines, errorStyle.Render("Error: "+m.errMsg))
	}

	// Agent status lives below the input box (not above it), so it reads
	// as "what will the next message use" right next to where the next
	// message gets typed. Shows the model the current agent resolves to
	// instead of the Tab-cycle hint — the model is what actually answers
	// the next message, and Tab's behavior doesn't need restating here on
	// every single frame.
	footer := "agent: " + m.currentAgent
	if model, ok := m.currentModel(); ok {
		footer += "  ·  model: " + model
	}
	lines = append(lines, statusStyle.Render(footer))

	v := tea.NewView(strings.Join(lines, "\n"))
	// Alt screen is a property of the frame in bubbletea v2, not a program
	// option, so it's declared here rather than at tea.NewProgram.
	v.AltScreen = true

	// Put the *physical* terminal cursor at the text insertion point inside
	// the prompt box. Terminals draw IME composition ("marked text" — a
	// Hangul syllable mid-assembly, kana being converted, pinyin) at the
	// physical cursor, so without this the half-finished characters render
	// wherever the cursor happened to be parked, which is the end of the
	// frame: the footer line *below* the prompt box. They then jumped up
	// into the box only once the syllable committed and arrived as a real
	// key event. textarea.Cursor() reports a position relative to the
	// prompt box itself, so it needs the box's own row added to it.
	if cur := m.input.Cursor(); cur != nil {
		cur.Position.Y += inputRow
		v.Cursor = cur
	}
	return v
}
