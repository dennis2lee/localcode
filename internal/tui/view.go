package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// turnLabel is what the separator above a user turn says. One word, and
// it is the only thing left naming the speaker now that the inline
// "You: " prefix is gone.
const turnLabel = "You"

// fallbackWidth is the column count assumed when the viewport has not
// been sized yet, which it has not been before the first resize message.
// One constant rather than one number here and another in
// refreshViewport, because a transcript wrapped to a different width
// than the box it is put in wraps twice.
const fallbackWidth = 80

// minTurnWidth is the narrowest a user block is laid out at: one column
// for the "▌", one for the space after it, and one for a character of
// the prompt. Below that lipgloss has nothing to wrap into. It only
// bites on a terminal narrow enough that nothing would be readable
// anyway; it is here so a hostile width cannot panic the repeat below.
const minTurnWidth = 3

// turnSeparator draws the boundary a user turn starts at, with the
// speaker's name on it.
//
// The rule is the same "─" run inputBorder already uses, so the two
// horizontal lines in this interface are the same line rather than two
// conventions that happen to look alike.
func turnSeparator(width int) string {
	rule := width - lipgloss.Width(turnLabel) - 1
	if rule < 0 {
		rule = 0
	}
	return statusStyle.Render(turnLabel + " " + strings.Repeat("─", rule))
}

// renderTranscript styles every entry by kind and joins them with the blank
// line that has always separated transcript paragraphs — this is the one
// place transcriptEntry becomes ANSI text, so a style change never means
// touching the append call sites in transcript.go/events.go.
//
// width is the column the transcript is laid out in. It is needed because
// a user turn is wrapped here rather than by the viewport: the gutter has
// to be drawn on every line of the block, and a wrap that happens after
// the border is applied would put it on the first line only.
func renderTranscript(entries []transcriptEntry, width int) string {
	if len(entries) == 0 {
		return ""
	}
	if width <= 0 {
		width = fallbackWidth
	}
	if width < minTurnWidth {
		width = minTurnWidth
	}
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		// The separator below owns the spacing between entries, so an
		// entry's own leading/trailing newlines are stripped first. Model
		// replies almost always end with one, which stacked on top of the
		// separator and left two blank lines between paragraphs instead of
		// one. Only newlines are trimmed, never other whitespace — the
		// indentation of a code line the model emitted is content.
		text := strings.Trim(e.text, "\n")
		if text == "" {
			continue
		}
		switch e.kind {
		case entryUser:
			parts = append(parts, turnSeparator(width)+"\n"+userStyle.Width(width).Render(text))
		case entryPending:
			// The same block, dimmed: it says "on its way" without moving
			// or changing shape when the real one replaces it.
			parts = append(parts, turnSeparator(width)+"\n"+pendingStyle.Width(width).Render(text))
		case entryTool, entryLocal:
			parts = append(parts, toolStyle.Render(text))
		default: // entryModel: streamed as-is, no style
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
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
	// A picker takes the transcript's room while it is open. See
	// picker.go for why that is the right place for it.
	body := m.viewport.View()
	if m.picker != nil {
		body = m.pickerView(m.viewport.Width(), m.viewport.Height())
	}
	lines := strings.Split(body, "\n")

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
		lines = append(lines, modalStyle.Render(m.pending.prompt(strings.TrimSpace(m.input.Value()) != "")))
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
	// The completion hint replaces the agent line while a "/name" is
	// being typed: it is about the key you are deciding whether to press,
	// and the agent is not going anywhere.
	if hint := m.completionHint(); hint != "" && m.picker == nil {
		footer = hint
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
