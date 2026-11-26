package tui

import (
	"strings"
	"testing"

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
