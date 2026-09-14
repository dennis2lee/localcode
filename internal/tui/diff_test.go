package tui

import (
	"fmt"
	"strings"
	"testing"

	"localcode/internal/events"
)

// Driving one tool call through the event handlers below is the
// requirement, not the implementation: a reader of the transcript
// learns what an edit changed from the "- "/"+" rows, whatever
// function computes them.
func applyToolCall(m *Model, id, name, input, content string, isError bool) {
	m.applyEvent(events.Event{Type: events.TypeToolStart, Data: map[string]any{
		"tool_use_id": id, "name": name, "input": input,
	}})
	m.applyEvent(events.Event{Type: events.TypeToolEnd, Data: map[string]any{
		"tool_use_id": id, "content": content, "is_error": isError,
	}})
}

func TestAnEditLeavesAMinusPlusDiffInTheTranscript(t *testing.T) {
	m := newTestModel()
	applyToolCall(&m, "e1", "edit",
		`{"path":"main.go","old_string":"x := 1\n","new_string":"x := 2\n"}`,
		"replaced 1 occurrence(s) in main.go", false)
	got := m.transcriptText()
	if !strings.Contains(got, "- x := 1") {
		t.Errorf("transcript has no removed line:\n%s", got)
	}
	if !strings.Contains(got, "+ x := 2") {
		t.Errorf("transcript has no added line:\n%s", got)
	}
}

func TestAToolDiffCoversBeforeAndAfterAroundTheChange(t *testing.T) {
	for _, tc := range []struct {
		name     string
		old, new string
		want     []string
	}{
		{"a one-line swap", "a\nOLD\nb", "a\nNEW\nb",
			[]string{"  a", "- OLD", "+ NEW", "  b"}},
		{"a pure addition", "a\nb", "a\nNEW\nb",
			[]string{"  a", "+ NEW", "  b"}},
		{"a pure removal", "a\nOLD\nb", "a\nb",
			[]string{"  a", "- OLD", "  b"}},
		{"new content at the end", "a", "a\nb",
			[]string{"  a", "+ b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := diffToolLines(tc.old, tc.new)
			var got []string
			for _, r := range rows {
				mark := "  "
				if r.kind == "del" {
					mark = "- "
				} else if r.kind == "add" {
					mark = "+ "
				}
				got = append(got, mark+r.text)
			}
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("diff = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNoDiffWhenThereIsNothingToShow(t *testing.T) {
	for _, tc := range []struct {
		name             string
		tool             string
		input            string
		content          string
		isError          bool
		wantTranscript   string
		wantNoTranscript string
	}{
		{
			"a failed edit changed nothing",
			"edit", `{"path":"m.go","old_string":"a","new_string":"b"}`,
			"old_string not found in m.go", true,
			"▸ edit", "- a",
		},
		{
			"a replaced file keeps its old text off the wire",
			"write_file", `{"path":"o.go","content":"package main\n"}`,
			"replaced o.go entirely: it had 340 line(s), it now has 1 (13 bytes). Everything the file previously held is gone.", false,
			"▸ write_file", "+ package main",
		},
		{
			"other tools never diff",
			"bash", `{"command":"go test ./..."}`,
			"ok", false,
			"▸ bash", "- ",
		},
		{
			"an edit that changed nothing",
			"edit", `{"path":"s.go","old_string":"x","new_string":"x"}`,
			"replaced 1 occurrence(s) in s.go", false,
			"▸ edit", "- x",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel()
			applyToolCall(&m, "c1", tc.tool, tc.input, tc.content, tc.isError)
			got := m.transcriptText()
			if !strings.Contains(got, tc.wantTranscript) {
				t.Errorf("transcript = %q, want it to hold %q", got, tc.wantTranscript)
			}
			if strings.Contains(got, tc.wantNoTranscript) {
				t.Errorf("transcript = %q, want no %q in it", got, tc.wantNoTranscript)
			}
		})
	}
}

func TestACreatedFileShowsItsLinesAsAdded(t *testing.T) {
	m := newTestModel()
	applyToolCall(&m, "w1", "write_file",
		`{"path":"new.go","content":"package main\n\nfunc main() {}\n"}`,
		"created new.go: 3 line(s), 29 bytes", false)
	got := m.transcriptText()
	for _, want := range []string{"+ package main", "+ func main() {}"} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript = %q, want it to hold %q", got, want)
		}
	}
	if strings.Contains(got, "- ") {
		t.Errorf("transcript = %q, want no removed lines for a new file", got)
	}
}

func TestAFiftyLineDiffDoesNotPushTheConversationOffTheScreen(t *testing.T) {
	var oldL, newL []string
	for i := 0; i < 50; i++ {
		oldL = append(oldL, fmt.Sprintf("old %d", i))
		newL = append(newL, fmt.Sprintf("new %d", i))
	}
	m := newTestModel()
	applyToolCall(&m, "e1", "edit",
		fmt.Sprintf(`{"path":"big.go","old_string":%q,"new_string":%q}`,
			strings.Join(oldL, "\n"), strings.Join(newL, "\n")),
		"replaced 1 occurrence(s) in big.go", false)
	got := m.transcriptText()
	lines := strings.Count(got, "\n")
	if lines > toolDiffMaxLines+4 {
		t.Errorf("transcript grew by %d lines for one edit, want at most %d plus the count line", lines, toolDiffMaxLines)
	}
	if !strings.Contains(got, "more diff lines, not shown") {
		t.Errorf("transcript = %q, want the count of what did not fit", got)
	}
	// The cap cuts rows, not honesty: the head of the change is shown —
	// here the removed block, with the added block behind the cut.
	if !strings.Contains(got, "- old 0") {
		t.Errorf("transcript = %q, want the head of the diff before the cut", got)
	}
}

func TestADiffAddsNoStylingToTheStoredTranscript(t *testing.T) {
	m := newTestModel()
	applyToolCall(&m, "e1", "edit",
		`{"path":"m.go","old_string":"x := 1","new_string":"x := 2"}`,
		"replaced 1 occurrence(s) in m.go", false)
	_ = renderTranscript(m.transcript, 80)
	for _, e := range m.transcript {
		if strings.Contains(e.text, "\x1b") {
			t.Errorf("escape sequence stored in the transcript entry: %q", e.text)
		}
	}
}

func TestACancelledEditLeavesNothingBehindForTheNextCall(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypeToolStart, Data: map[string]any{
		"tool_use_id": "e1", "name": "edit",
		"input": `{"path":"m.go","old_string":"a","new_string":"b"}`,
	}})
	m.applyEvent(events.Event{Type: events.TypeTurnCancelled})
	if len(m.pendingTools) != 0 {
		t.Errorf("pendingTools = %v, want the cancelled call dropped with the turn", m.pendingTools)
	}
	// An end arriving late for a dropped call must not draw a diff for
	// whatever happens to reuse the conversation afterwards.
	m.applyEvent(events.Event{Type: events.TypeToolEnd, Data: map[string]any{
		"tool_use_id": "e1", "content": "replaced 1 occurrence(s) in m.go", "is_error": false,
	}})
	if strings.Contains(m.transcriptText(), "- a") {
		t.Errorf("transcript = %q, want no diff from a call the turn already dropped", m.transcriptText())
	}
}

func TestTwoEditsInFlightKeepTheirOwnInputs(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypeToolStart, Data: map[string]any{
		"tool_use_id": "e1", "name": "edit",
		"input": `{"path":"a.go","old_string":"aaa","new_string":"AAA"}`,
	}})
	m.applyEvent(events.Event{Type: events.TypeToolStart, Data: map[string]any{
		"tool_use_id": "e2", "name": "edit",
		"input": `{"path":"b.go","old_string":"bbb","new_string":"BBB"}`,
	}})
	// The second finishes first; each end must still diff its own call.
	m.applyEvent(events.Event{Type: events.TypeToolEnd, Data: map[string]any{
		"tool_use_id": "e2", "content": "replaced 1 occurrence(s) in b.go", "is_error": false,
	}})
	m.applyEvent(events.Event{Type: events.TypeToolEnd, Data: map[string]any{
		"tool_use_id": "e1", "content": "replaced 1 occurrence(s) in a.go", "is_error": false,
	}})
	got := m.transcriptText()
	for _, want := range []string{"- aaa", "+ AAA", "- bbb", "+ BBB"} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript = %q, want it to hold %q", got, want)
		}
	}
}
