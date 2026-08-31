package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/events"
)

// Reading back through a conversation.
//
// Until these keys existed there was no way to do it at all: the viewport
// held the whole transcript, but Update was never called on it, so the
// scroll bindings bubbles ships never saw a keypress — and the frame sets
// AltScreen, which takes the terminal's own scrollback away too. Anything
// past the last screenful was unreachable while sitting in memory.
func TestTheTranscriptCanBeScrolledBack(t *testing.T) {
	m := longConversation(t)

	if m.viewport.AtBottom() != true {
		t.Fatal("a fresh transcript should be at the bottom")
	}
	before := m.viewport.YOffset()

	updated, _, handled := m.handleKey(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if !handled {
		t.Fatal("PgUp was not handled, so nothing scrolls the transcript")
	}
	m = updated.(Model)
	if m.viewport.YOffset() >= before {
		t.Errorf("PgUp left the offset at %d (was %d): the view did not move", m.viewport.YOffset(), before)
	}

	// And back down again, or the reader is stuck at the top instead of
	// the bottom, which is no better.
	up := m.viewport.YOffset()
	updated, _, handled = m.handleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if !handled {
		t.Fatal("PgDown was not handled")
	}
	m = updated.(Model)
	if m.viewport.YOffset() <= up {
		t.Errorf("PgDown left the offset at %d (was %d)", m.viewport.YOffset(), up)
	}
}

func TestShiftArrowsScrollOneLine(t *testing.T) {
	m := longConversation(t)
	before := m.viewport.YOffset()

	updated, _, handled := m.handleKey(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift})
	if !handled {
		t.Fatal("shift+up was not handled")
	}
	m = updated.(Model)
	if m.viewport.YOffset() != before-1 {
		t.Errorf("shift+up moved the offset from %d to %d, want one line", before, m.viewport.YOffset())
	}

	updated, _, handled = m.handleKey(tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift})
	if !handled {
		t.Fatal("shift+down was not handled")
	}
	m = updated.(Model)
	if m.viewport.YOffset() != before {
		t.Errorf("shift+down left the offset at %d, want back at %d", m.viewport.YOffset(), before)
	}
}

// Plain Up and Down still belong to history recall and to moving around a
// multi-line prompt. Taking them for scrolling would trade something
// people do every minute for something they do occasionally.
func TestPlainArrowsAreNotScrollKeys(t *testing.T) {
	m := longConversation(t)
	before := m.viewport.YOffset()

	updated, _, _ := m.handleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	m = updated.(Model)
	if m.viewport.YOffset() != before {
		t.Errorf("plain Up scrolled the transcript, from %d to %d", before, m.viewport.YOffset())
	}
}

// Growing the prompt box shrinks the viewport, and the offset's ceiling
// comes down with it. SetHeight leaves the offset where it was, so it
// could end up past the end: AtBottom() is then false forever, and since
// refreshViewport only follows new output while that is true, the
// transcript stopped moving and looked frozen.
func TestShrinkingTheViewportCannotStrandTheOffset(t *testing.T) {
	m := longConversation(t)

	// At the bottom, then the prompt box grows into a tall paste.
	m.viewport.GotoBottom()
	m.input.SetValue(strings.Repeat("a line of a pasted block\n", 12))
	m.resizeLayout()

	if !m.viewport.AtBottom() {
		t.Errorf("offset %d is past the end after the viewport shrank: the transcript will stop following output",
			m.viewport.YOffset())
	}
}

// longConversation returns a model whose transcript is comfortably taller
// than its viewport, which is the only state in which scrolling means
// anything.
func longConversation(t *testing.T) Model {
	t.Helper()
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	for i := 0; i < 200; i++ {
		m.applyEvent(events.Event{
			Type: events.TypeUserMessage,
			Data: map[string]any{"text": "message number " + string(rune('a'+i%26))},
		})
	}
	m.refreshViewport()
	if m.viewport.TotalLineCount() <= m.viewport.Height() {
		t.Fatalf("transcript is %d lines in a %d-line viewport: nothing to scroll",
			m.viewport.TotalLineCount(), m.viewport.Height())
	}
	return m
}
