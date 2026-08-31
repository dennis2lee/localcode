package tui

import tea "charm.land/bubbletea/v2"

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
	// Whether the reader was following the newest output has to survive the
	// resize, and it does not survive on its own.
	//
	// SetHeight leaves the offset alone, but the offset's ceiling is the
	// content height minus the visible height — so shrinking the viewport,
	// which is what growing the prompt box does, moves the bottom down and
	// away from wherever the offset is. Someone who was following output
	// was then silently no longer at the bottom, and refreshViewport only
	// follows while AtBottom() is true: the transcript stopped moving and
	// looked frozen, with no key to unfreeze it because until recently
	// there were no scroll keys either.
	//
	// Following, so follow. Otherwise re-clamp, since the ceiling can
	// equally have come down and left the offset above it.
	atBottom := m.viewport.AtBottom()
	m.viewport.SetHeight(vh)
	if atBottom {
		m.viewport.GotoBottom()
	} else {
		m.viewport.SetYOffset(m.viewport.YOffset())
	}
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

// handleWindowSize applies a terminal resize: the viewport/input widths,
// a fresh layout pass, and a re-wrap of the existing transcript, which was
// wrapped for whatever width was current the last time something was
// appended — a resize just invalidated that.
func (m Model) handleWindowSize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.termHeight = msg.Height
	m.viewport.SetWidth(msg.Width)
	m.input.SetWidth(msg.Width - 2)
	m.resizeLayout()
	m.refreshViewport()
	return m, nil
}
