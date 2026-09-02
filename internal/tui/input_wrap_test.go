package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Typing into the prompt box, one key at a time, past the width of the
// box.
//
// No test did this before, which is how a prompt-scrambling bug lived in
// the box for as long as it did: every other test puts text in with
// SetValue, and SetValue does not go through the per-keystroke layout
// pass that was doing the damage. Once a prompt soft-wrapped, every
// keystroke after it was inserted back near the start of the line, so
// "the quick brown fox jumps over the lazy dog" came out as "over the
// lazy dogthe quick brown fox jumps ".
func typeInto(m Model, s string) Model {
	for _, r := range s {
		updated, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated.(Model)
	}
	return m
}

func TestALongPromptSurvivesBeingTyped(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 30, Height: 24})
	m = updated.(Model)

	want := "the quick brown fox jumps over the lazy dog"
	m = typeInto(m, want)

	if got := m.input.Value(); got != want {
		t.Errorf("typed  %q\nwanted %q", got, want)
	}
}

// The same thing in Korean, where it arrives sooner: a Hangul syllable is
// two cells wide, so a prompt wraps at half the characters.
func TestALongKoreanPromptSurvivesBeingTyped(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 30, Height: 24})
	m = updated.(Model)

	want := "메일을 읽고, 철수에게서 온 게 있으면 정리해라"
	m = typeInto(m, want)

	if got := m.input.Value(); got != want {
		t.Errorf("typed  %q\nwanted %q", got, want)
	}
}

// And a prompt typed across several lines with ctrl+j, which is the case
// the layout pass exists for: the box grows, and the cursor has to come
// back to the line it was on rather than to a count of visual rows.
func TestAMultiLinePromptSurvivesBeingTyped(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	m = updated.(Model)

	for i, line := range []string{"first line", "second line", "third line"} {
		if i > 0 {
			u, _ := m.Update(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
			m = u.(Model)
		}
		m = typeInto(m, line)
	}

	want := "first line\nsecond line\nthird line"
	if got := m.input.Value(); got != want {
		t.Errorf("typed  %q\nwanted %q", got, want)
	}
	if got, want := m.input.LineCount(), 3; got != want {
		t.Errorf("LineCount = %d, want %d", got, want)
	}
	if !strings.HasSuffix(m.input.Value(), "third line") {
		t.Errorf("the cursor did not stay on the last line: %q", m.input.Value())
	}
}

// The other half of the same fix, and the reason the guard is worth
// having on top of the column one: the layout pass sends the box's
// internal viewport back to the top, which for a prompt taller than the
// box scrolls the cursor out of sight. Typing then works and is
// invisible, which is its own kind of broken.
func TestTheCursorStaysInSightWhileTypingPastTheWrap(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 30, Height: 24})
	m = updated.(Model)
	m = typeInto(m, "the quick brown fox jumps over the lazy dog")

	top := m.input.ScrollYOffset()
	row := m.input.LineInfo().RowOffset
	if row < top || row >= top+m.input.Height() {
		t.Errorf("the cursor is on row %d, outside the %d rows shown from %d",
			row, m.input.Height(), top)
	}
}
