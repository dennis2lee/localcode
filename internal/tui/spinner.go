package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

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

// formatElapsed renders a duration the way the busy indicator says it:
// whole seconds, minutes, or hours, never a decimal. Sub-second precision
// would churn the line every frame for no information — whether a tool
// has run 3.1 or 3.9 seconds never changed what anybody did about it.
func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Truncate(time.Second)
	h := int(d.Hours())
	min := int(d.Minutes()) % 60
	sec := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%dm%ds", h, min, sec)
	case min > 0:
		return fmt.Sprintf("%dm%ds", min, sec)
	default:
		return fmt.Sprintf("%ds", sec)
	}
}

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
		// Thinking first, and only until a tool starts: reasoning happens
		// before the model asks for anything, so "thinking" while a tool
		// runs would name the wrong half of the turn.
		if m.thinking {
			what = "thinking"
		}
		if m.runningTool != "" {
			what = m.runningTool
			// How long it has been at it, which is the piece of this
			// line people actually wait on. Guarded on the timestamp
			// rather than assumed from the name: a tool.start that
			// arrived without one (or a test that sets the name
			// directly) still names the tool, it just has no age.
			if !m.toolStartedAt.IsZero() {
				what += " " + formatElapsed(time.Since(m.toolStartedAt))
			}
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
