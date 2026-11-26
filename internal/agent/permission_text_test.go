package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"localcode/internal/events"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// A permission question shows what the person approves, all of it, and
// nothing a screen would act on rather than draw. See permission_text.go
// and events.VisibleText, which is tested here so that internal/events
// stays a package with no tests of its own.

func TestVisibleTextEscapesWhatAScreenWouldActOn(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"run the tests", "run the tests"},
		// Kept: a prompt of several paragraphs has to read as one.
		{"first\n\nsecond\tcolumn", "first\n\nsecond\tcolumn"},
		// And kept beside something that is escaped, which is the path a
		// text with only breaks and tabs never takes.
		{"one\r\ntwo\tthree", `one\r` + "\ntwo\tthree"},
		{"run tests\rHIDDEN", `run tests\rHIDDEN`},
		{"a\x1b[8mMORE\x1b[0m", `a\x1b[8mMORE\x1b[0m`},
		{"bell\x07 del\x7f", `bell\x07 del\x7f`},
		{"c1\u0085", `c1\u0085`},
		{"\u202eABC\u202c", `\u202eABC\u202c`},
		{"zero\u200bwidth\ufeff", `zero\u200bwidth\ufeff`},
		{"line\u2028para\u2029", `line\u2028para\u2029`},
		{"bad\xffbyte", `bad\xffbyte`},
		// Drawn as nothing, though not format characters: what Unicode
		// calls default-ignorable, and the variation selectors.
		{"cgj\u034fvs\ufe0fvs17\U000e0100", `cgj\u034fvs\ufe0fvs17\U000e0100`},
		{"fill\u115f\u1160\u3164\uffa0", `fill\u115f\u1160\u3164\uffa0`},
		{"khmer\u17b4\u17b5mongol\u180b", `khmer\u17b4\u17b5mongol\u180b`},
		// Past the basic plane in Go's own spelling, not a five-digit \u.
		{"tag\U000e0041", `tag\U000e0041`},
		// Drawn, so kept: a combining mark, and the Braille blank.
		{"strike\u0336blank\u2800", "strike\u0336blank\u2800"},
		{"한국어 그대로", "한국어 그대로"},
	} {
		if got := events.VisibleText(tc.in); got != tc.want {
			t.Errorf("VisibleText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Through the broker, because that is the one place every question passes:
// what reaches the log is what both clients draw.
func TestAPermissionRequestIsWrittenWithItsControlCharactersEscaped(t *testing.T) {
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	if _, err := store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	broker := NewPermissionBroker(store)
	restore := SetUnattendedWait(0)
	defer restore()

	ctx := WithUnattended(WithSessionID(context.Background(), "s1"))
	_, _ = broker.Func()(ctx, tools.Ask{
		Tool: "Schedule", Subject: "in 1 hour: run",
		Description: "schedule for 09:00: run tests\rHIDDEN\x1b[8mMORE\x1b[0m\u202eABC\n\nsecond paragraph",
	})

	var got string
	evs, err := store.Events("s1", 0)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	for _, ev := range evs {
		if ev.Type == events.TypePermissionRequest {
			got, _ = ev.Data["description"].(string)
		}
	}
	want := `schedule for 09:00: run tests\rHIDDEN\x1b[8mMORE\x1b[0m\u202eABC` + "\n\nsecond paragraph"
	if got != want {
		t.Errorf("the request was written as %q, want %q", got, want)
	}
}

func TestTheScheduleAndDebateQuestionsShowTheWholeText(t *testing.T) {
	loop := scheduleLoop(t)
	const prompt = "run the tests\n\nthen do the second thing"

	sched, _ := json.Marshal(map[string]string{"when": "in 1 hour", "prompt": prompt})
	if got := NewScheduleTool(loop).Describe(sched); !strings.Contains(got, prompt) {
		t.Errorf("the Schedule question is %q, want the whole prompt in it", got)
	}
	// Permission rules match on Subject, so it stays the one line it was.
	if got := NewScheduleTool(loop).Subject(sched); strings.Contains(got, "second thing") {
		t.Errorf("Subject changed with the question: %q", got)
	}
	debate, _ := json.Marshal(map[string]any{"reviewers": []string{"explore"}, "rounds": 1, "task": prompt})
	if got := NewDebateTool(loop).Describe(debate); !strings.Contains(got, prompt) {
		t.Errorf("the Debate question is %q, want the whole task in it", got)
	}

	// Past the bound the question says exactly how much it left out.
	long := strings.Repeat("x", maxConfirmationRunes+37)
	huge, _ := json.Marshal(map[string]string{"when": "in 1 hour", "prompt": long})
	got := NewScheduleTool(loop).Describe(huge)
	if !strings.Contains(got, "(37 more characters not shown)") {
		t.Errorf("a prompt past the bound does not say what was left out: …%s", got[len(got)-60:])
	}
	if strings.Count(got, "x") != maxConfirmationRunes {
		t.Errorf("the question shows %d characters of the prompt, want %d", strings.Count(got, "x"), maxConfirmationRunes)
	}
}

// A refusal names the question it could not ask, in one line. The question
// carries up to 8,000 characters now, and handing all of them back to the
// model that wrote them is noise in its tool result.
func TestAnUnattendedRefusalNamesTheQuestionInOneLine(t *testing.T) {
	restore := SetUnattendedWait(0)
	defer restore()
	long := "schedule for 09:00: " + strings.Repeat("x", 300) + "\n\nthe second paragraph"
	got := unattendedRefusal(tools.Ask{Tool: "Schedule", Description: long})
	if strings.Contains(got, "second paragraph") {
		t.Errorf("the refusal repeats the whole question: %q", got)
	}
	if !strings.Contains(got, "schedule for 09:00: xxx") || !strings.Contains(got, "…") {
		t.Errorf("the refusal does not name the question by its first line, cut: %q", got)
	}
	if n := strings.Count(got, "x"); n > 200 {
		t.Errorf("the refusal carries %d characters of the question, want at most 200", n)
	}
	if short := unattendedRefusal(tools.Ask{Tool: "write_file", Description: "write /tmp/x"}); !strings.Contains(short, `"write /tmp/x"`) {
		t.Errorf("a one-line question is not named whole: %q", short)
	}
}

// memory_save showed 600 bytes of a note, which split a Korean or emoji
// character in half and said nothing about what it left out.
func TestAMemoryNoteIsShownWholeInItsQuestion(t *testing.T) {
	content := "a" + strings.Repeat("가", 250) + "끝"
	input, _ := json.Marshal(map[string]string{"scope": "project", "name": "n", "content": content})
	got := MemorySaveTool{}.Describe(input)
	if !strings.Contains(got, content) {
		t.Errorf("the memory question does not carry the whole note: …%q", got[len(got)-40:])
	}
	if !utf8.ValidString(got) {
		t.Errorf("the memory question is not valid UTF-8: it split a character")
	}
}
