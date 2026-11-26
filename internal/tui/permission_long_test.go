package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

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
		if w := len([]rune(l)); w > 80 {
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
