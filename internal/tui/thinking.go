package tui

import (
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

// The muse reasoning block, the terminal's half.
//
// The TUI used to draw no reasoning at all: "thinking" on the busy line,
// then the answer. For a muse model that meant a long silence and no way
// to tell afterwards that the reply had been preceded by anything. The
// daemon now marks a muse stream's reasoning with "fold" (see
// foldsThinking in internal/agent), and for those the transcript gets a
// block of its own: while it streams, a header with its running time and
// the last few lines of the reasoning; once the answer starts, one line
// saying how long it took. Ctrl+O opens every folded block and folds
// them again.
//
// Other models keep what they had, the busy line's word and nothing in
// the transcript. Nothing here is replayed: the daemon never logs
// reasoning, so a block exists only in the client that watched it arrive.

// thinkingTailLines is how much of a live block is on screen: enough to
// see that it moves and roughly what about, few enough that the answer
// below it is not pushed off the screen while it is still being thought.
const thinkingTailLines = 6

var (
	thinkingHeadStyle   = lipgloss.NewStyle().Faint(true).Italic(true)
	thinkingGutterStyle = lipgloss.NewStyle().Faint(true)
	thinkingBodyStyle   = lipgloss.NewStyle().Faint(true).Italic(true)
)

// liveThinking is the index of the reasoning block still streaming, or
// -1. Searched from the end rather than remembered, because an entry can
// leave the middle of the transcript (a prompt's echo resolving) and an
// index kept across that would point at the wrong line.
//
// thinkingLive says whether there may be one, so the search runs only
// then: it is asked on every answer delta, and on every reasoning delta
// of a model the fold does not apply to, and a long conversation with no
// block open should not be walked each time. A search that finds nothing
// clears it, so a transcript replaced under it costs one walk.
func (m *Model) liveThinking() int {
	if !m.thinkingLive {
		return -1
	}
	for i := len(m.transcript) - 1; i >= 0; i-- {
		if e := m.transcript[i]; e.kind == entryThinking && e.live {
			return i
		}
	}
	m.thinkingLive = false
	return -1
}

// appendThinkingDelta adds reasoning to the block streaming now, opening
// one when there is none.
func (m *Model) appendThinkingDelta(text string) {
	if i := m.liveThinking(); i >= 0 {
		m.transcript[i].text += text
		m.transcript[i].note = thinkingLiveNote(time.Since(m.thinkingSince))
		m.transcriptRev++
		return
	}
	// Not opened on whitespace alone: a block whose reasoning is a
	// couple of newlines shows nothing, and would still count as one for
	// Ctrl+O.
	if strings.TrimSpace(text) == "" {
		return
	}
	m.thinkingSince = time.Now()
	m.thinkingLive = true
	block := transcriptEntry{kind: entryThinking, text: text, live: true, note: thinkingLiveNote(0)}
	if last := len(m.transcript) - 1; m.streamOpen && last >= 0 {
		// The model reasons after it has started answering. The block
		// goes in front of the answer, which is where reasoning sits in
		// the message, and the answer stays the open entry: splitting
		// it here would have message.part.end write the whole reply
		// again below the block, the part above it included.
		m.transcript = append(m.transcript[:last], block, m.transcript[last])
	} else {
		m.transcript = append(m.transcript, block)
	}
	m.transcriptRev++
}

// foldThinking closes the block streaming now, if there is one. elapsed
// is the daemon's figure, from the block's first delta to its end; zero
// when the block ended without saying (the answer started, the turn
// stopped), and then this client's own clock is the next best thing.
func (m *Model) foldThinking(elapsed time.Duration) {
	i := m.liveThinking()
	if i < 0 {
		return
	}
	if elapsed <= 0 {
		elapsed = time.Since(m.thinkingSince)
	}
	m.thinkingLive = false
	e := &m.transcript[i]
	e.live = false
	e.note = "Thought for " + formatElapsed(elapsed)
	e.open = m.thinkingExpanded
	m.transcriptRev++
}

// tickThinking moves the live block's clock, and reports whether the
// line changed: once a second, not once a frame.
func (m *Model) tickThinking() bool {
	i := m.liveThinking()
	if i < 0 {
		return false
	}
	note := thinkingLiveNote(time.Since(m.thinkingSince))
	if m.transcript[i].note == note {
		return false
	}
	m.transcript[i].note = note
	m.transcriptRev++
	return true
}

// toggleThinkingBlocks is Ctrl+O: every folded block opens, or every one
// closes, and blocks folded later follow the same choice. It reports
// whether there was any block to act on; with none, nothing changes, so
// a key pressed in the wrong conversation does not leave the next block
// open.
func (m *Model) toggleThinkingBlocks() bool {
	want := !m.thinkingExpanded
	found := false
	for i := range m.transcript {
		e := &m.transcript[i]
		if e.kind != entryThinking || e.live || !transcriptEntryShows(*e) {
			continue
		}
		found = true
		if e.open != want {
			e.open = want
			m.transcriptRev++
		}
	}
	if found {
		m.thinkingExpanded = want
	}
	return found
}

func thinkingLiveNote(d time.Duration) string {
	return "Thinking · " + formatElapsed(d)
}

// renderThinking draws one reasoning block at width.
func renderThinking(e transcriptEntry, width int) string {
	marker, hint := "▸", "  (ctrl+o to show)"
	switch {
	case e.live:
		marker, hint = "▾", ""
	case e.open:
		marker, hint = "▾", "  (ctrl+o to fold)"
	}
	head := thinkingHeadStyle.Render(marker + " " + e.note + hint)
	if !e.live && !e.open {
		return head
	}
	body := strings.Trim(e.text, "\n")
	inner := width - 2
	if inner < 1 {
		inner = 1
	}
	if e.live {
		body = thinkingTail(body, inner)
	}
	lines := strings.Split(thinkingBodyStyle.Width(inner).Render(body), "\n")
	if e.live && len(lines) > thinkingTailLines {
		lines = lines[len(lines)-thinkingTailLines:]
	}
	gutter := thinkingGutterStyle.Render("│ ")
	for i, l := range lines {
		lines[i] = gutter + l
	}
	return head + "\n" + strings.Join(lines, "\n")
}

// thinkingTail cuts a long reasoning down to its end before it is
// wrapped. A live block shows only its last few lines, and wrapping all
// of a reasoning that has run to thousands of words on every delta would
// cost the whole of what the render cache exists to save. Twice the
// bytes the lines could hold, so a line of two-cell, three-byte
// characters still has enough behind it.
func thinkingTail(text string, width int) string {
	limit := width * thinkingTailLines * 2
	if limit < 512 {
		limit = 512
	}
	if len(text) <= limit {
		return text
	}
	cut := len(text) - limit
	for cut < len(text) && !utf8.RuneStart(text[cut]) {
		cut++
	}
	return text[cut:]
}
