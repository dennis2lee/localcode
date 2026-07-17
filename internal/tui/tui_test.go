package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"localcode/internal/client"
	"localcode/internal/events"
)

// newTestModel builds a Model without touching the network — fine here
// since these tests only drive Update()/resizeLayout() directly, never
// Init() or a real tea.Program, so the client is never called.
func newTestModel() Model {
	ch := make(chan events.Event)
	return New(client.New("http://unused.invalid"), "s1", ch)
}

func TestResizeLayoutGrowsWithContent(t *testing.T) {
	m := newTestModel()

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	if got, want := m.input.LineCount(), 1; got != want {
		t.Fatalf("initial LineCount = %d, want %d", got, want)
	}
	initialViewportHeight := m.viewport.Height
	if initialViewportHeight != 24-2-1 {
		t.Errorf("initial viewport height = %d, want %d", initialViewportHeight, 24-2-1)
	}

	m.input.SetValue("line one\nline two\nline three\nline four")
	m.resizeLayout()

	if got, want := m.input.Height(), 4; got != want {
		t.Errorf("input height after 4-line content = %d, want %d", got, want)
	}
	if m.viewport.Height >= initialViewportHeight {
		t.Errorf("viewport height = %d, expected it to shrink below the 1-line baseline %d", m.viewport.Height, initialViewportHeight)
	}
	if want := 24 - 2 - 4; m.viewport.Height != want {
		t.Errorf("viewport height = %d, want %d", m.viewport.Height, want)
	}
}

func TestResizeLayoutClampsToMaxHeight(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	huge := ""
	for i := 0; i < inputMaxHeight+10; i++ {
		huge += "line\n"
	}
	m.input.SetValue(huge)
	m.resizeLayout()

	if got := m.input.Height(); got != inputMaxHeight {
		t.Errorf("input height = %d, want it clamped to inputMaxHeight = %d", got, inputMaxHeight)
	}
	if m.viewport.Height < 3 {
		t.Errorf("viewport height = %d, want it floored at 3 even when input maxes out", m.viewport.Height)
	}
}

func TestResizeLayoutShrinksBackAfterClear(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	m.input.SetValue("line one\nline two\nline three")
	m.resizeLayout()
	grownHeight := m.viewport.Height

	m.input.Reset()
	m.resizeLayout()

	if m.input.LineCount() != 1 {
		t.Fatalf("expected LineCount 1 after Reset, got %d", m.input.LineCount())
	}
	if m.viewport.Height <= grownHeight {
		t.Errorf("viewport height = %d, expected it to grow back above the grown-input height %d", m.viewport.Height, grownHeight)
	}
}
