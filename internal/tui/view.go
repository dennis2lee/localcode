package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// renderTranscript styles every entry by kind and joins them with the blank
// line that has always separated transcript paragraphs — this is the one
// place transcriptEntry becomes ANSI text, so a style change never means
// touching the append call sites in transcript.go/events.go.
func renderTranscript(entries []transcriptEntry) string {
	if len(entries) == 0 {
		return ""
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
			parts = append(parts, userStyle.Render("You: ")+text)
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
