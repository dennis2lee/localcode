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

	const chromeLines = 2 // status/permission line + blank separator
	// What the band below the prompt box really takes, once it is more
	// than the two rows always kept for it: a wrapped or long permission
	// question, the requests queued behind it, the busy line. Without it
	// the frame was taller than the terminal and the alternate screen cut
	// the bottom rows, which are the keys that answer the question.
	m.bandRows = m.statusBandRows()
	reserved := chromeLines
	if m.bandRows > reserved {
		reserved = m.bandRows
	}
	// The transcript keeps three rows and the prompt box what it holds,
	// except while a permission question is on screen. Then both give way,
	// the transcript to none and the prompt box to one row, before the keys
	// that answer the question go past the bottom of a short terminal.
	floor := 3
	if m.pending != nil {
		floor = 0
		if room := m.termHeight - reserved - borderLines - footerLines - floor; inputHeight > room {
			inputHeight = max(1, room)
		}
	}
	m.input.SetHeight(inputHeight)
	m.scrollInputToTop()
	vh := m.termHeight - reserved - borderLines - footerLines - inputHeight
	if vh < floor {
		vh = floor
	}
	// A drag holds a place on a track of the old height. Ended, as a
	// resize of the terminal ends it.
	if vh != m.viewport.Height() {
		m.scrollbarDragging = false
		m.scrollbarDragGrab = 0
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
	// Both halves of this comparison have to be visual rows, and one of
	// them was not. LineCount is logical lines: a single prompt long
	// enough to soft-wrap counts as one, so a box one row tall holding a
	// two-row prompt looked like content that fits and this ran anyway —
	// resetting the cursor to a row-relative column on every keystroke,
	// which scrambled the prompt from the wrap point on. LineInfo's
	// Height is the rows the cursor's line actually occupies.
	if m.input.LineCount() > m.input.Height() || m.input.LineInfo().Height > m.input.Height() {
		return // genuinely taller than the box; the offset is doing real work
	}
	row := m.input.Line()
	// Column, not LineInfo's ColumnOffset: ColumnOffset is measured from
	// the start of the wrapped row and SetCursorColumn wants it from the
	// start of the line. They agree only where nothing wraps.
	col := m.input.Column()
	m.input.MoveToBegin() // scrolls the internal viewport back to line 0
	// CursorDown steps a visual row, which is not a line, so step until
	// the line is the one we left rather than counting presses. It stops
	// on its own at the end of the text, where a press moves nothing.
	for m.input.Line() < row {
		wasRow, wasCol := m.input.Line(), m.input.Column()
		m.input.CursorDown()
		if m.input.Line() == wasRow && m.input.Column() == wasCol {
			break
		}
	}
	m.input.SetCursorColumn(col)
}

// handleWindowSize applies a terminal resize: the viewport/input widths,
// a fresh layout pass, and a re-wrap of the existing transcript, which was
// wrapped for whatever width was current the last time something was
// appended — a resize just invalidated that.
func (m Model) handleWindowSize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.termHeight = msg.Height
	m.termWidth = msg.Width
	m.viewport.SetWidth(msg.Width)
	m.input.SetWidth(msg.Width - 2)
	// A resize moves what every scrollbar row means, so a drag held
	// across one ends rather than landing somewhere it never pointed.
	m.scrollbarDragging = false
	m.scrollbarDragGrab = 0
	m.resizeLayout()
	m.refreshViewport()
	return m, nil
}

// Update is the one door every change to the model comes through, which is
// why the layout is checked here: the band below the prompt box grows and
// shrinks with a permission question, a queue, a busy turn or an error,
// and each of those arrives by a different path. The transcript is resized
// when, and only when, the band's height has changed.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	if nm, ok := next.(Model); ok && nm.termHeight > 0 && nm.statusBandRows() != nm.bandRows {
		nm.resizeLayout()
		// The transcript is wrapped again for the height it now has: whether
		// it overflows decides whether the scrollbar takes a column.
		nm.refreshViewport()
		return nm, cmd
	}
	return next, cmd
}
