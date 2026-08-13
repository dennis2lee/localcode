package tui

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

// recordHistory adds a prompt this client did not submit: the ones the
// daemon replays when the TUI attaches to an existing session, and any sent
// from another client while it is attached. Attaching to a conversation and
// finding Up empty is the case this exists for — the prompts are right
// there in the transcript above the box.
//
// Unlike rememberPrompt it leaves navigation alone: an event arriving while
// someone is walking back through history must not empty the box under
// them, and appending at the end cannot move the entries they are looking
// at.
func (m *Model) recordHistory(text string) {
	if text == "" {
		return
	}
	if n := len(m.history); n > 0 && m.history[n-1] == text {
		return
	}
	composing := m.historyIdx >= len(m.history)
	m.history = append(m.history, text)
	if composing {
		m.historyIdx = len(m.history)
	}
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
