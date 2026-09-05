package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// A picker is the TUI's answer to the two things the Web UI has had all
// along and this client did not: a way to see what you can switch to
// before you switch to it.
//
// Both existed here only as a name you had to already know. "/agent
// <name>" needs the name; sessions could be chosen once, on the listing
// printed before the program starts, and never again. So the ways to
// change either were "type it exactly" and "restart", which is a fair
// description of the gap between this client and the other one.
//
// It draws over the transcript rather than under the prompt box. The
// permission prompt and the busy line live in the band below, and they
// are one line each about something happening right now; a list you are
// choosing from wants the room, and taking the transcript's space for
// the moment you are choosing costs nothing, since the transcript is
// still there when you come back.

// pickerItem is one selectable row: what to do with it, what to show,
// and a dimmer second column for the detail that helps you choose.
type pickerItem struct {
	id     string
	label  string
	detail string
}

// picker is an open selection list. A nil *picker means no picker, which
// is what every other part of Model tests.
type picker struct {
	title string
	items []pickerItem
	idx   int
	// onPick is what selecting a row does. Returning a tea.Cmd rather
	// than performing the switch here keeps the picker a widget: it
	// knows how to choose, not what choosing means.
	onPick func(m *Model, it pickerItem) tea.Cmd
}

// pickerVisibleRows bounds how many rows are drawn at once. A session
// list can be long, and a list taller than the terminal is not a list.
const pickerVisibleRows = 12

// move steps the selection, stopping at the ends rather than wrapping.
// Wrapping is right for Tab cycling through two or three agents and
// wrong here: jumping from the last session to the first looks like the
// list moved rather than the cursor.
func (p *picker) move(delta int) {
	p.idx += delta
	if p.idx < 0 {
		p.idx = 0
	}
	if p.idx > len(p.items)-1 {
		p.idx = len(p.items) - 1
	}
}

// window returns the slice of items to draw and the index of the
// selected one within it, scrolling only when the selection would leave
// the visible rows.
func (p *picker) window(height int) ([]pickerItem, int) {
	if height <= 0 || len(p.items) <= height {
		return p.items, p.idx
	}
	start := p.idx - height/2
	if start < 0 {
		start = 0
	}
	if start > len(p.items)-height {
		start = len(p.items) - height
	}
	return p.items[start : start+height], p.idx - start
}

// openPicker installs a picker, or reports why there is nothing to pick
// from. The message goes to the transcript rather than the error line
// because it is an answer to what was just typed, not a fault.
func (m *Model) openPicker(p *picker, empty string) tea.Cmd {
	if len(p.items) == 0 {
		m.appendLocal(empty)
		return nil
	}
	m.picker = p
	return nil
}

// handlePickerKey is the whole keyboard while a picker is open. Every
// key is consumed, including the ones that would otherwise type into the
// prompt box: the box is not what has focus, and letters landing in it
// behind a list is the kind of thing you only discover after sending
// them.
func (m Model) handlePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.picker = nil
		return m, nil, true
	case "up", "ctrl+p":
		m.picker.move(-1)
		return m, nil, true
	case "down", "ctrl+n":
		m.picker.move(1)
		return m, nil, true
	case "pgup":
		m.picker.move(-pickerVisibleRows)
		return m, nil, true
	case "pgdown":
		m.picker.move(pickerVisibleRows)
		return m, nil, true
	case "home":
		m.picker.idx = 0
		return m, nil, true
	case "end":
		m.picker.idx = len(m.picker.items) - 1
		return m, nil, true
	case "enter":
		p := m.picker
		it := p.items[p.idx]
		m.picker = nil
		return m, p.onPick(&m, it), true
	}
	return m, nil, true
}

// pickerView renders the list into exactly height rows, so the frame it
// replaces keeps its shape and nothing below it moves.
func (m Model) pickerView(width, height int) string {
	p := m.picker
	if width <= 0 {
		width = 40
	}
	lines := make([]string, 0, height)
	lines = append(lines, modalStyle.Render(p.title))
	lines = append(lines, statusStyle.Render("↑/↓ to choose, Enter to select, Esc to cancel"))
	lines = append(lines, "")

	rows := height - len(lines) - 1
	if rows > pickerVisibleRows {
		rows = pickerVisibleRows
	}
	items, sel := p.window(rows)
	for i, it := range items {
		text := it.label
		if it.detail != "" {
			text += "  " + it.detail
		}
		text = truncate(text, width-4)
		if i == sel {
			lines = append(lines, userStyle.Render("> "+text))
			continue
		}
		lines = append(lines, "  "+toolStyle.Render(text))
	}
	if len(p.items) > rows {
		lines = append(lines, statusStyle.Render(fmt.Sprintf("  (%d of %d)", p.idx+1, len(p.items))))
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines[:height], "\n")
}

// truncate cuts a row to fit, counting runes rather than bytes so a
// Korean or Japanese label is not cut mid-character. It does not account
// for double-width cells, so a full-width line can still overrun by a
// column or two; that is a display nicety, and cutting a character in
// half is not.
// shortenPath cuts a long path from the front, keeping the tail.
//
// The mirror of shortenPath in the Web UI's format.js, and for the same
// reason its stylesheet gives: the tail is the project directory, which
// is the part that identifies a session. The picker's own truncate cuts
// from the right, so a raw absolute path put in a row's detail loses
// exactly the half worth having — and, before that, pushes whatever
// follows it off the line.
func shortenPath(path string, max int) string {
	r := []rune(path)
	if len(r) <= max {
		return path
	}
	tail := string(r[len(r)-(max-1):])
	// Prefer starting at a separator so the result reads as a path rather
	// than a word chopped in half.
	if cut := strings.IndexByte(tail, '/'); cut > 0 && cut < 8 {
		tail = tail[cut:]
	}
	return "…" + tail
}

func truncate(s string, width int) string {
	if width <= 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return string(r[:width-1]) + "…"
}
