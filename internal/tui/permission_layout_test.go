package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"localcode/internal/client"
	"localcode/internal/events"
)

// prompt is the whole modal as one string, head and keys, for the tests
// that read it. The product draws the two halves apart; see questionBlock.
func (p pendingPermission) prompt(typing bool) string {
	head, keys := p.parts(typing)
	return head + "\n" + keys
}

// Every terminal of nine rows or more, in every combination the band can
// be in, shows the keys that answer a permission question, inside its
// height and its width.
//
// A sweep rather than cases, because four review rounds each found the next
// size or combination the cases before them missed: a short terminal, a
// draft of several lines, a model's question beside the permission one, an
// error of several lines, a rule that wraps, a queue. Below nine rows the
// question's start is cut, and USAGE says so.
func TestEveryTerminalOfNineRowsShowsTheKeys(t *testing.T) {
	questions := []struct {
		name string
		ev   events.Event
	}{
		{"short", permissionEvent("p1", "bash git status")},
		{"boundary", events.Event{Type: events.TypePermissionRequest, Data: map[string]any{
			"id": "p1", "tool": "write_file", "description": "write /tmp/a.txt",
			"outside": "write", "outside_dir": "/Users/alice/other-project", "workspace": "/Users/alice/my-project"}}},
		{"long with a rule", events.Event{Type: events.TypePermissionRequest, Data: map[string]any{
			"id": "p1", "tool": "write_file", "description": "write /tmp/long\nsecond line",
			"rule": "/Users/alice/workspace/project/internal/service/handler.go", "can_always": true}}},
	}
	const (
		busy = 1 << iota
		queue
		draft
		asking
		multilineError
		all = busy | queue | draft | asking | multilineError
	)
	for _, w := range []int{30, 40, 60, 80, 120} {
		for h := 9; h <= 30; h++ {
			for _, q := range questions {
				for mods := 0; mods <= all; mods++ {
					m := sized(t, w, h)
					if mods&busy != 0 {
						m.waiting = true
					}
					if mods&draft != 0 {
						m.input.SetValue("draft one\ndraft two\ndraft three")
						m.resizeLayout()
					}
					if mods&asking != 0 {
						m = deliver(t, m, events.Event{Type: events.TypeInputRequest, Data: map[string]any{
							"id": "a1", "question": "Which one?", "options": []any{"first", "second", "third", "fourth"}}})
					}
					if mods&multilineError != 0 {
						m.errMsg = "line 1\nline 2\nline 3\nline 4"
					}
					m = deliver(t, m, q.ev)
					if mods&queue != 0 {
						m = deliver(t, m, permissionEvent("p2", "second queued request"))
					}

					name := fmt.Sprintf("%dx%d %s mods=%05b", w, h, q.name, mods)
					rows := strings.Split(m.View().Content, "\n")
					if len(rows) > h {
						t.Errorf("%s: the frame is %d rows", name, len(rows))
						continue
					}
					screen := ansi.Strip(strings.Join(rows, "\n"))
					if !strings.Contains(screen, "y: allow") || !strings.Contains(screen, "n: deny") {
						t.Errorf("%s: the keys are not on screen:\n%s", name, screen)
					}
					for _, r := range rows {
						// The model's own question is drawn unwrapped, as it
						// was before; that line is its business, not this one's.
						if ansi.StringWidth(r) > w && !strings.Contains(ansi.Strip(r), "The model is asking") {
							t.Errorf("%s: a row is %d columns: %q", name, ansi.StringWidth(r), ansi.Strip(r))
						}
					}
				}
			}
		}
	}
}

// The other findings of the fourth round, one each.

// Typing re-lays the band out on its own; the transcript's width follows.
func TestTypingUnderAQuestionKeepsTheTranscriptWidthRight(t *testing.T) {
	m := New(client.New("http://unused.invalid"), "s1", "general-purpose", make(chan events.Event), true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 23})
	m = updated.(Model)
	for i := 0; i < 6; i++ {
		m = deliver(t, m, events.Event{Type: events.TypeUserMessage, Data: map[string]any{"text": fmt.Sprintf("msg %d", i+1)}})
	}
	m = deliver(t, m, permissionEvent("p1", "bash git status"))
	for _, key := range []tea.KeyPressMsg{{Code: 'x', Text: "x"}, {Code: tea.KeyBackspace}} {
		updated, _ = m.Update(key)
		m = updated.(Model)
		for i, r := range strings.Split(m.View().Content, "\n") {
			if w := ansi.StringWidth(r); w > 80 {
				t.Errorf("after %v: row %d is %d columns", key, i, w)
			}
		}
		if _, drawn := m.scrollbarLayout(); !drawn && m.viewport.Width() != 80 {
			t.Errorf("after %v: no scrollbar and a transcript %d columns wide", key, m.viewport.Width())
		}
	}
}

// A transcript of no rows reserves no scrollbar column.
func TestATranscriptOfNoRowsReservesNoScrollbarColumn(t *testing.T) {
	m := New(client.New("http://unused.invalid"), "s1", "general-purpose", make(chan events.Event), true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	m = updated.(Model)
	for i := 0; i < 4; i++ {
		m = deliver(t, m, events.Event{Type: events.TypeUserMessage, Data: map[string]any{"text": fmt.Sprintf("msg %d", i+1)}})
	}
	m = deliver(t, m, events.Event{Type: events.TypePermissionRequest, Data: map[string]any{
		"id": "p1", "tool": "write_file", "description": "write /tmp/long\nsecond line",
		"rule": "/Users/alice/workspace/project/internal/service/handler.go", "can_always": true}})
	if m.viewport.Height() == 0 && m.viewport.Width() != 40 {
		t.Errorf("a transcript of no rows is %d columns, want 40", m.viewport.Width())
	}
}

// The rule is cut before an escape it would split.
func TestARuleIsNotCutInsideAnEscape(t *testing.T) {
	for _, rule := range []string{
		strings.Repeat("x", 118) + `‮` + "end",
		strings.Repeat("x", 119) + `\x1b` + "end",
		"a" + strings.Repeat("\n", 60),
	} {
		got := ruleHint(rule)
		cut, _, _ := strings.Cut(got, "…")
		if i := strings.LastIndex(cut, `\`); i >= 0 {
			tail := cut[i:]
			if tail == `\` || (len(tail) < 6 && strings.HasPrefix(tail, `\u`)) || (len(tail) < 4 && strings.HasPrefix(tail, `\x`)) {
				t.Errorf("ruleHint(%.20q…) cut an escape: %q", rule, got)
			}
		}
	}
}

// The directory and workspace of a boundary question draw their breaks and
// tabs as escapes, as the rest of the modal's one-line fields do.
func TestBoundaryPathsDrawTheirBreaksAsEscapes(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypePermissionRequest, Data: map[string]any{
		"id": "p1", "tool": "write_file", "description": "write /tmp/x",
		"outside": "write", "outside_dir": "/tmp/a\tb\nc", "workspace": "/work/x\ty\nz"}})
	p := m.pending.prompt(false)
	if strings.Contains(p, "\t") || strings.Contains(p, "/tmp/a\tb") || strings.Contains(p, "b\nc") || strings.Contains(p, "y\nz") {
		t.Errorf("a boundary path keeps a raw tab or break: %q", p)
	}
}

// A picker open under a question that leaves the transcript no rows draws
// none of its own either, so the frame still fits.
func TestAPickerUnderAQuestionStillFits(t *testing.T) {
	m := sized(t, 40, 10)
	m.openPicker(&picker{
		title: "Sessions",
		items: []pickerItem{{id: "s1", label: "Session 1"}, {id: "s2", label: "Session 2"}},
	}, "empty")
	m = deliver(t, m, events.Event{Type: events.TypePermissionRequest, Data: map[string]any{
		"id": "p1", "tool": "write_file", "description": "write /tmp/long\nsecond line",
		"rule": "/Users/alice/workspace/project/internal/service/handler.go", "can_always": true}})
	if rows := strings.Split(m.View().Content, "\n"); len(rows) > 10 {
		t.Errorf("the frame is %d rows on a 10-row terminal with a picker open", len(rows))
	}
}
