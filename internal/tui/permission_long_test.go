package tui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"localcode/internal/client"
	"localcode/internal/events"
)

// A question the footer cannot hold is written whole into the transcript,
// which scrolls and wraps, before it can be answered. The footer keeps one
// line of it, says where the rest is, and keeps its keys.

func permissionEvent(id, desc string) events.Event {
	return events.Event{Type: events.TypePermissionRequest, Data: map[string]any{
		"id": id, "tool": "Schedule", "description": desc, "once": true,
	}}
}

func TestAQuestionTooLongForTheFooterIsWrittenWholeIntoTheTranscript(t *testing.T) {
	for _, tc := range []struct {
		name, desc string
	}{
		{"two paragraphs", "schedule for 09:00: run the tests\n\nthen do the second thing"},
		{"one line past the footer", "schedule for 09:00: " + strings.Repeat("word ", 30) + "the second thing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel()
			m.applyEvent(permissionEvent("p1", tc.desc))

			if got := m.transcriptText(); !strings.Contains(got, tc.desc) {
				t.Errorf("the transcript does not hold the whole question:\n%s", got)
			}
			if m.pending == nil {
				t.Fatal("the question is not on screen")
			}
			footer := m.pending.prompt(false)
			if !strings.Contains(footer, "the whole request is in the transcript above") {
				t.Errorf("the footer does not say where the rest is:\n%s", footer)
			}
			if strings.Contains(footer, "the second thing") {
				t.Errorf("the footer carries the whole question, which it cannot show:\n%s", footer)
			}
			if !strings.Contains(footer, "y: allow") {
				t.Errorf("the footer lost its keys:\n%s", footer)
			}
		})
	}
}

func TestAShortQuestionStaysInTheFooterAlone(t *testing.T) {
	m := newTestModel()
	before := len(m.transcript)
	m.applyEvent(permissionEvent("p1", "bash git status"))
	if len(m.transcript) != before {
		t.Errorf("a one-line question was written into the transcript too: %q", m.transcriptText())
	}
	if footer := m.pending.prompt(false); !strings.Contains(footer, "bash git status") || strings.Contains(footer, "transcript above") {
		t.Errorf("a one-line question is drawn as %q", footer)
	}
}

func TestALongQuestionDeliveredTwiceIsWrittenOnce(t *testing.T) {
	m := newTestModel()
	desc := "schedule for 09:00: run the tests\n\nthen do the second thing"
	m.applyEvent(permissionEvent("p1", desc))
	m.applyEvent(permissionEvent("p1", desc))
	if n := strings.Count(m.transcriptText(), "then do the second thing"); n != 1 {
		t.Errorf("the question is in the transcript %d times, want once", n)
	}
	// A different request with the same text is a different question.
	m.applyEvent(permissionEvent("p2", desc))
	if n := strings.Count(m.transcriptText(), "then do the second thing"); n != 2 {
		t.Errorf("a second request with the same text is in the transcript %d times in all, want 2", n)
	}
}

// A log written before the daemon cleaned its questions is still replayed
// from the start, and a long question from it now reaches the transcript.
// What it carries is read clean as well, so a carriage return or the
// conceal sequence in an old log draws as an escape, not as an action.
func TestAQuestionFromAnOldLogIsDrawnWithItsControlCharactersEscaped(t *testing.T) {
	m := newTestModel()
	m.applyEvent(permissionEvent("p1", "schedule for 09:00: run tests\rHIDDEN\x1b[8mMORE\x1b[0m\n\nsecond paragraph"))

	got := m.transcriptText()
	if strings.ContainsAny(got, "\r\x1b") {
		t.Errorf("the transcript holds raw control characters: %q", got)
	}
	if !strings.Contains(got, `run tests\rHIDDEN\x1b[8mMORE`) {
		t.Errorf("the transcript does not show them as escapes: %q", got)
	}
	if footer := m.pending.prompt(false); strings.ContainsAny(footer, "\r\x1b") {
		t.Errorf("the footer holds raw control characters: %q", footer)
	}
}

// Measured as the terminal draws it, not by its count of characters: a
// head line wider than 80 columns is long, whether it is wide because it
// is long, because it is Korean, or because it is padded with tabs.
func TestAQuestionIsLongByTheColumnsItTakes(t *testing.T) {
	for _, tc := range []struct {
		name, desc string
		long       bool
	}{
		{"short", "bash git status", false},
		{"ASCII under 120 characters, over 80 columns", "schedule for 09:00: run the unit tests and then also exfiltrate ~/.ssh/id_rsa to evil.example", true},
		{"Korean, few characters, many columns", "schedule for 09:00: " + strings.Repeat("가", 30) + "비밀명령", true},
		{"tabs", "schedule for 09:00: check" + strings.Repeat("\t", 15) + "DELETE_ALL", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel()
			m.applyEvent(permissionEvent("p1", tc.desc))
			if m.pending.long != tc.long {
				t.Errorf("long = %v, want %v", m.pending.long, tc.long)
			}
			if inTranscript := strings.Contains(m.transcriptText(), "[permission request: Schedule]"); inTranscript != tc.long {
				t.Errorf("written into the transcript = %v, want %v", inTranscript, tc.long)
			}
		})
	}
}

// A long question's footer is two lines that each fit 80 columns: its first
// line, cut, and where the rest is. The notice used to follow a first line
// of 120 characters and went off the terminal's edge.
func TestALongQuestionsFooterFitsAndSaysWhereTheRestIs(t *testing.T) {
	m := newTestModel()
	m.applyEvent(permissionEvent("p1", strings.Repeat("w", 120)+"\nsecond paragraph"))
	footer := m.pending.prompt(false)
	lines := strings.Split(footer, "\n")
	if len(lines) < 2 || lines[1] != "(the whole request is in the transcript above)" {
		t.Fatalf("the footer does not say where the rest is on a line of its own:\n%s", footer)
	}
	for _, l := range lines {
		if w := lipgloss.Width(l); w > 80 {
			t.Errorf("a footer line is %d columns wide: %q", w, l)
		}
	}
}

// A narrow terminal wraps a short question instead of cutting it.
func TestTheFooterWrapsToTheTerminal(t *testing.T) {
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	m = updated.(Model)
	m.applyEvent(permissionEvent("p1", "write /tmp/a/file/with/a/long/name ENDMARK"))
	view := m.View().Content
	if !strings.Contains(view, "ENDMARK") {
		t.Errorf("a question on a 40-column terminal lost its end:\n%s", view)
	}
	// The view holds a line whole even when the terminal cuts it, so what
	// is checked is that no line of the question is wider than the terminal.
	for _, l := range strings.Split(view, "\n") {
		if (strings.Contains(l, "Permission request") || strings.Contains(l, "ENDMARK")) && lipgloss.Width(l) > 40 {
			t.Errorf("a line of the question is %d columns on a 40-column terminal: %q", lipgloss.Width(l), l)
		}
	}
}

// The transcript a long question was written into is cleared on a session
// switch, and so is the record of having written it: the conversation is
// replayed from its start into a new transcript.
func TestALongQuestionIsWrittenAgainAfterASessionSwitch(t *testing.T) {
	m := newTestModel()
	desc := "schedule for 09:00: run the tests\n\nthen the second thing"
	m.applyEvent(permissionEvent("p1", desc))
	for _, sid := range []string{"s2", "s1"} {
		updated, _ := m.Update(sessionSwitchedMsg{sessionID: sid, agent: "general-purpose", events: make(chan events.Event)})
		m = updated.(Model)
	}
	m.applyEvent(permissionEvent("p1", desc))
	if !strings.Contains(m.transcriptText(), "then the second thing") {
		t.Errorf("after switching back, the question is not in the transcript the footer points to:\n%s", m.transcriptText())
	}
}

// An id is a request until it is answered. After that, and under a new
// text, the same id is a new question; and a request with no id is never
// taken for another.
func TestTheSameIdIsANewQuestionOnceAnsweredOrChanged(t *testing.T) {
	m := newTestModel()
	m.applyEvent(permissionEvent("p1", "old task\n\nold paragraph"))
	m.applyEvent(events.Event{Type: events.TypePermissionResolved, Data: map[string]any{"id": "p1"}})
	m.applyEvent(permissionEvent("p1", "new task\n\nNEW PARAGRAPH"))
	if !strings.Contains(m.transcriptText(), "NEW PARAGRAPH") {
		t.Error("a request reusing an answered id is not written into the transcript")
	}
	// The same text again under the answered id is a new question too, and
	// is written where it is asked rather than left to the copy above.
	m.applyEvent(events.Event{Type: events.TypePermissionResolved, Data: map[string]any{"id": "p1"}})
	m.applyEvent(permissionEvent("p1", "new task\n\nNEW PARAGRAPH"))
	if n := strings.Count(m.transcriptText(), "NEW PARAGRAPH"); n != 2 {
		t.Errorf("the same question asked again under an answered id is in the transcript %d times, want 2", n)
	}
	m.applyEvent(permissionEvent("p1", "newer task\n\nCHANGED PARAGRAPH"))
	if !strings.Contains(m.transcriptText(), "CHANGED PARAGRAPH") {
		t.Error("a changed text under the same id is not written into the transcript")
	}
	m.applyEvent(permissionEvent("", "no id\n\nSAME TEXT"))
	m.applyEvent(permissionEvent("", "no id\n\nSAME TEXT"))
	if n := strings.Count(m.transcriptText(), "SAME TEXT"); n != 2 {
		t.Errorf("two requests with no id are in the transcript %d times, want 2: one is taken for the other", n)
	}
}

// The other fields a question draws are read clean too. They are display
// copies: an answer names the request by id.
func TestTheBoundaryFieldsOfAQuestionAreDrawnClean(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypePermissionRequest, Data: map[string]any{
		"id": "p1", "tool": "write_file", "description": "write /tmp/x/f.txt",
		"outside": "write", "outside_dir": "/tmp/\x1b[8msecret\r/tmp/safe\u202e",
		"workspace": "/work/\x1b[31mproj", "rule": "/tmp/\u202ex", "can_always": true,
	}})
	prompt := m.pending.prompt(false)
	if strings.ContainsAny(prompt, "\x1b\r\u202e") {
		t.Errorf("the question draws raw control characters: %q", prompt)
	}
	for _, want := range []string{`/tmp/\x1b[8msecret\r/tmp/safe\u202e`, `/work/\x1b[31mproj`} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the question does not show %s as escapes: %q", want, prompt)
		}
	}
}

// sized is a model laid out for a terminal of w by h, as a resize gives it.
func sized(t *testing.T, w, h int) Model {
	t.Helper()
	m := newTestModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return updated.(Model)
}

// deliver sends one event the way the stream does, through Update.
func deliver(t *testing.T, m Model, ev events.Event) Model {
	t.Helper()
	updated, _ := m.Update(eventMsg{ev: ev, gen: m.streamGen})
	return updated.(Model)
}

// The frame fits the terminal. The band below the prompt box grows with a
// wrapped or long question, its rule and the requests behind it, and the
// transcript gives up the rows: the alternate screen cuts what is past the
// last one, and those are the keys that answer the question.
func TestThePermissionBandTakesItsRowsFromTheTranscript(t *testing.T) {
	for _, tc := range []struct {
		name  string
		w, h  int
		ev    events.Event
		queue bool
		wants []string
	}{
		{"a long question with its rule and a queue, 80 by 24", 80, 24,
			events.Event{Type: events.TypePermissionRequest, Data: map[string]any{
				"id": "p1", "tool": "write_file", "description": "write /tmp/long\nsecond line",
				"rule": "/Users/alice/workspace/project/internal/service/handler.go", "can_always": true,
			}}, true,
			[]string{"y: allow", "handler.go", "1 more permission request waiting", "transcript above"}},
		{"a short question wrapped on 28 columns", 28, 24,
			permissionEvent("p1", "write /tmp/a/long/path/to/file/name/here ENDMARK"), false,
			[]string{"ENDMARK", "y: allow"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := sized(t, tc.w, tc.h)
			m.waiting = true
			m = deliver(t, m, tc.ev)
			if tc.queue {
				m = deliver(t, m, permissionEvent("p2", "second queued request"))
			}
			view := m.View().Content
			rows := strings.Split(view, "\n")
			if len(rows) > tc.h {
				t.Errorf("the frame is %d rows on a %d-row terminal", len(rows), tc.h)
			}
			shown := strings.Join(rows[:min(len(rows), tc.h)], "\n")
			for _, want := range tc.wants {
				if !strings.Contains(shown, want) {
					t.Errorf("%q is past the last row of the terminal:\n%s", want, shown)
				}
			}
		})
	}
	// And when the question is answered the transcript gets the rows back.
	m := sized(t, 80, 24)
	before := m.viewport.Height()
	m = deliver(t, m, permissionEvent("p1", "write /tmp/long\nsecond line"))
	if m.viewport.Height() >= before {
		t.Errorf("the transcript kept %d rows under a three-row question", m.viewport.Height())
	}
	m = deliver(t, m, events.Event{Type: events.TypePermissionResolved, Data: map[string]any{"id": "p1"}})
	if m.viewport.Height() != before {
		t.Errorf("the transcript has %d rows after the question was answered, want %d back", m.viewport.Height(), before)
	}
}

// The rule is drawn once escaped. It is read through VisibleText, and %q
// on top of that wrote every backslash of the escape twice.
func TestTheRuleIsEscapedOnce(t *testing.T) {
	rlo := string(rune(0x202e))
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypePermissionRequest, Data: map[string]any{
		"id": "p1", "tool": "write_file", "description": "write /tmp/x",
		"rule": "/tmp/" + rlo + "x", "can_always": true,
	}})
	prompt := m.pending.prompt(false)
	want := `a: always allow "/tmp/` + "\\" + `u202ex"`
	if !strings.Contains(prompt, want) {
		t.Errorf("the rule is drawn as %q, want it to contain %q", prompt, want)
	}
	if strings.Contains(prompt, rlo) {
		t.Errorf("the rule is drawn raw: %q", prompt)
	}
}

// The tool's name is part of the head: a tab in it is drawn as spaces and
// a line break in it makes the question long. A long name leaves the head
// inside 80 columns all the same.
func TestTheToolNameCountsInTheHead(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypePermissionRequest, Data: map[string]any{"id": "p1", "tool": "my\ttool", "description": "short"}})
	if strings.Contains(m.pending.prompt(false), "\t") {
		t.Errorf("a tab in the tool name reaches the footer: %q", m.pending.prompt(false))
	}
	m = newTestModel()
	m.applyEvent(events.Event{Type: events.TypePermissionRequest, Data: map[string]any{"id": "p1", "tool": "foo\nbar", "description": "short"}})
	if !m.pending.long {
		t.Error("a tool name with a line break is not a long question")
	}
	m = newTestModel()
	m.applyEvent(events.Event{Type: events.TypePermissionRequest, Data: map[string]any{
		"id": "p1", "tool": "mcp__filesystem__write_file_contents", "description": strings.Repeat("w", 120) + "\nsecond",
	}})
	head, _, _ := strings.Cut(m.pending.prompt(false), "\n")
	if w := lipgloss.Width(head); w > 80 {
		t.Errorf("the head of a long question with a long tool name is %d columns: %q", w, head)
	}
}

// A lost turn takes its questions with it, so one asked again under a
// reused id is written into the transcript again.
func TestALostTurnForgetsWhatItWroteIntoTheTranscript(t *testing.T) {
	m := newTestModel()
	m.waiting = true
	desc := "schedule for 09:00: run\n\nsecond"
	m.applyEvent(permissionEvent("p1", desc))
	m.endLostTurn()
	m.applyEvent(permissionEvent("p1", desc))
	if n := strings.Count(m.transcriptText(), "second"); n != 2 {
		t.Errorf("the question asked again after a lost turn is in the transcript %d times, want 2", n)
	}
}

// A boundary question is one of two words. Anything else is not one, as
// the Web UI already takes it, and is not drawn into the keys.
func TestAnUnknownBoundaryWordIsNotABoundaryQuestion(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypePermissionRequest, Data: map[string]any{
		"id": "p1", "tool": "write_file", "description": "write /tmp/x",
		"outside": "write" + string(rune(0x1b)) + "[8m", "outside_dir": "/tmp/a", "workspace": "/work/b",
	}})
	if p := m.pending.prompt(false); strings.Contains(p, string(rune(0x1b))) || strings.Contains(p, "d: allow") {
		t.Errorf("an unknown boundary word is drawn into the keys: %q", p)
	}
}

// An answer the daemon says is for nothing pending drops the question, and
// a later request under its id is a new one.
func TestAQuestionTheDaemonNoLongerHoldsIsForgotten(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"permission request p1 is not pending"}`)
	}))
	defer srv.Close()
	m := New(client.New(srv.URL), "s1", "general-purpose", make(chan events.Event), false)
	desc := "schedule for 09:00: run\n\nsecond"
	m.applyEvent(permissionEvent("p1", desc))
	m.pendingSince = time.Now().Add(-time.Hour)
	next, cmd, handled := press(m, "n")
	if !handled || cmd == nil {
		t.Fatal("n did not answer the question")
	}
	updated, _ := next.Update(cmd())
	m = updated.(Model)
	m.applyEvent(permissionEvent("p1", desc))
	if n := strings.Count(m.transcriptText(), "second"); n != 2 {
		t.Errorf("the question asked again after a not-pending answer is in the transcript %d times, want 2", n)
	}
}

// On a short terminal, or under a prompt box holding several lines, the
// transcript and the prompt box give up their rows before the keys do.
func TestTheKeysStayOnAShortTerminal(t *testing.T) {
	longRule := events.Event{Type: events.TypePermissionRequest, Data: map[string]any{
		"id": "p1", "tool": "write_file", "description": "write /tmp/long\nsecond line",
		"rule": "/Users/alice/workspace/project/internal/service/handler.go", "can_always": true,
	}}
	boundary := events.Event{Type: events.TypePermissionRequest, Data: map[string]any{
		"id": "p1", "tool": "write_file", "description": "write /tmp/a.txt",
		"outside": "write", "outside_dir": "/Users/alice/other-project", "workspace": "/Users/alice/my-project",
	}}
	for _, tc := range []struct {
		name  string
		w, h  int
		ev    events.Event
		draft int
	}{
		{"40x10", 40, 10, longRule, 0},
		{"40x12", 40, 12, longRule, 0},
		{"40x16 boundary", 40, 16, boundary, 0},
		{"80x10", 80, 10, longRule, 0},
		{"80x24 under a ten-line draft", 80, 24, boundary, 10},
		{"40x24 under a five-line draft", 40, 24, boundary, 5},
		// The prompt box has to give way here, not only the transcript.
		{"40x16 under a ten-line draft", 40, 16, boundary, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := sized(t, tc.w, tc.h)
			m.waiting = true
			if tc.draft > 0 {
				var ls []string
				for i := 0; i < tc.draft; i++ {
					ls = append(ls, fmt.Sprintf("draft %d", i+1))
				}
				m.input.SetValue(strings.Join(ls, "\n"))
				m.resizeLayout()
			}
			m = deliver(t, m, tc.ev)
			m = deliver(t, m, permissionEvent("p2", "second queued request"))
			rows := strings.Split(m.View().Content, "\n")
			if len(rows) > tc.h {
				t.Fatalf("the frame is %d rows on a %d-row terminal", len(rows), tc.h)
			}
			shown := strings.Join(rows, "\n")
			for _, want := range []string{"y: allow", "n: deny", "more permission request waiting"} {
				if !strings.Contains(shown, want) {
					t.Errorf("%q is not on screen:\n%s", want, shown)
				}
			}
		})
	}
}

// The rule an exact shell command grants is the command, lines and all.
// It is drawn on one line, its breaks and tabs as escapes, and cut.
func TestALongRuleIsOneLineOfTheKeys(t *testing.T) {
	var cmd []string
	for i := 0; i < 22; i++ {
		cmd = append(cmd, fmt.Sprintf("\tprint(%d)", i))
	}
	rule := "python3 - <<'EOF'\n" + strings.Join(cmd, "\n") + "\nEOF"
	m := sized(t, 80, 24)
	m = deliver(t, m, events.Event{Type: events.TypePermissionRequest, Data: map[string]any{
		"id": "p1", "tool": "bash", "description": "bash python3 -", "rule": rule, "can_always": true,
	}})
	m = deliver(t, m, permissionEvent("p2", "second queued request"))
	prompt := m.pending.prompt(false)
	_, keys, _ := strings.Cut(prompt, "\n")
	if strings.ContainsAny(keys, "\t") || strings.Count(keys, "\n") > 0 {
		t.Errorf("the keys line holds the rule's raw breaks or tabs: %q", keys)
	}
	if !strings.Contains(keys, "more characters)") {
		t.Errorf("a long rule is not cut with a count: %q", keys)
	}
	if rows := strings.Split(m.View().Content, "\n"); len(rows) > 24 || !strings.Contains(strings.Join(rows, "\n"), "more permission request waiting") {
		t.Errorf("the frame is %d rows, or the queue is off screen", len(rows))
	}
}

// A tool name with a line break is cut to its first line in a long
// question's head, so where the rest is stays the footer's second line.
func TestAToolNameWithABreakKeepsTheHeadOneLine(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypePermissionRequest, Data: map[string]any{"id": "p1", "tool": "foo\nbar\nbaz", "description": "short"}})
	lines := strings.Split(m.pending.prompt(false), "\n")
	if len(lines) < 2 || lines[1] != "(the whole request is in the transcript above)" {
		t.Errorf("the footer's second line is %q, want where the rest is", lines)
	}
}

// With the mouse on, the scrollbar takes a column when the transcript
// overflows. A question that changes the transcript's height changes
// whether it does, and the width follows; a drag on the old track ends.
func TestTheScrollbarFollowsTheBand(t *testing.T) {
	m := New(client.New("http://unused.invalid"), "s1", "general-purpose", make(chan events.Event), true)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)
	for i := 0; i < 6; i++ {
		m = deliver(t, m, events.Event{Type: events.TypeUserMessage, Data: map[string]any{"text": fmt.Sprintf("msg %d", i+1)}})
	}
	m.scrollbarDragging = true
	check := func(when string) {
		t.Helper()
		rows := strings.Split(m.View().Content, "\n")
		for i := 0; i <= m.viewport.Height() && i < len(rows); i++ {
			if w := lipgloss.Width(rows[i]); w > 80 {
				t.Errorf("%s: row %d is %d columns on an 80-column terminal", when, i, w)
				return
			}
		}
		if _, drawn := m.scrollbarLayout(); !drawn && m.viewport.Width() != 80 {
			t.Errorf("%s: no scrollbar, and the transcript is %d columns, want 80", when, m.viewport.Width())
		}
	}
	m = deliver(t, m, events.Event{Type: events.TypePermissionRequest, Data: map[string]any{
		"id": "p1", "tool": "write_file", "description": "write /tmp/a.txt",
		"outside": "write", "outside_dir": "/Users/alice/other-project", "workspace": "/Users/alice/my-project",
	}})
	check("with the question")
	if m.scrollbarDragging {
		t.Error("a drag survived the transcript changing height")
	}
	m = deliver(t, m, events.Event{Type: events.TypePermissionResolved, Data: map[string]any{"id": "p1"}})
	check("after the answer")
}
