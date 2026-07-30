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
