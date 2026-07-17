package tui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// pressEnterWith sets the input's content, then simulates pressing Enter
// exactly the way a real keypress would flow through Update.
func pressEnterWith(t *testing.T, m Model, text string) (Model, tea.Cmd) {
	t.Helper()
	m.input.SetValue(text)
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return updated.(Model), cmd
}

func isQuitCmd(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestExitCommandsQuit(t *testing.T) {
	for _, text := range []string{"exit", "EXIT", ":q", " :q "} {
		m := newTestModel()
		_, cmd := pressEnterWith(t, m, text)
		if !isQuitCmd(t, cmd) {
			t.Errorf("input %q: expected a quit command, got %v", text, cmd)
		}
	}
}

func TestHelpCommandRendersLocally(t *testing.T) {
	m := newTestModel()
	m, cmd := pressEnterWith(t, m, "/help")

	if cmd != nil {
		t.Errorf("/help should not issue a command (no server round trip), got %v", cmd)
	}
	if !strings.Contains(m.transcript.String(), "사용 가능한 명령") {
		t.Errorf("transcript = %q, want it to contain the help text", m.transcript.String())
	}
	if m.input.Value() != "" {
		t.Errorf("input should be cleared after /help, got %q", m.input.Value())
	}
}

func TestVersionCommandFetchesFromDaemon(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"version":"1.2.3"}`)
	}))
	defer srv.Close()

	m := New(client.New(srv.URL), "s1", make(chan events.Event))
	m, cmd := pressEnterWith(t, m, "/version")
	if cmd == nil {
		t.Fatal("/version should issue a command to fetch the daemon's version")
	}

	msg := cmd()
	updated, _ := m.Update(msg)
	m = updated.(Model)

	if !strings.Contains(m.transcript.String(), "1.2.3") {
		t.Errorf("transcript = %q, want it to contain the fetched version", m.transcript.String())
	}
}
