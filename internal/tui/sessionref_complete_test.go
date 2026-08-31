package tui

import (
	"strings"
	"testing"

	"localcode/internal/client"
	"localcode/internal/session"
)

// Completing "#<conversation>" with the right arrow.
//
// The same key as "/", and deliberately, but not the same rule. A command
// is the whole prompt, so completing one can replace the box. A reference
// is a word inside a sentence — "check #S2 against the file here" is the
// shape the feature exists for — so it has to be found where the cursor
// is and spliced back where it was.

func refModel(t *testing.T) Model {
	t.Helper()
	m := newTestModel()
	m.sessionID = "s-here"
	m.refNames = []session.Session{
		{ID: "s-here", Title: "this one"},
		{ID: "s-alpha", Title: "the parser rewrite"},
		{ID: "s-beta", Title: "the parser tests"},
		{ID: "s-gamma"},
	}
	return m
}

// complete presses Right once with the box holding text and the cursor at
// a rune offset, and reports what the box says afterwards.
func complete(m *Model, text string, cursor int) (string, int, bool) {
	return m.nextCompletion(text, cursor)
}

func TestAReferenceCompletesInTheMiddleOfASentence(t *testing.T) {
	m := refModel(t)
	typed := `check #"the parser r`
	got, at, ok := complete(&m, typed+" against the file here", len([]rune(typed)))
	if !ok {
		t.Fatal("the arrow did not complete")
	}
	want := `check #"the parser rewrite" against the file here`
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	// The cursor lands after the name rather than at the end of the box,
	// so typing carries on where the reference ended.
	if wantAt := len([]rune(`check #"the parser rewrite"`)); at != wantAt {
		t.Errorf("cursor = %d, want %d", at, wantAt)
	}
}

func TestATitleWithSpacesCompletesQuoted(t *testing.T) {
	m := refModel(t)
	got, _, ok := complete(&m, "#the", 4)
	if !ok || got != `#"the parser rewrite"` {
		t.Errorf("got %q ok=%v", got, ok)
	}
}

func TestTheWalkVisitsEveryConversationAndComesBack(t *testing.T) {
	m := refModel(t)
	text := `#"the parser`
	var seen []string
	for i := 0; i < 4; i++ {
		next, at, ok := complete(&m, text, len([]rune(text)))
		if !ok {
			t.Fatalf("press %d offered nothing", i)
		}
		seen = append(seen, next)
		text = next
		_ = at
	}
	want := []string{`#"the parser rewrite"`, `#"the parser tests"`, `#"the parser`, `#"the parser rewrite"`}
	if strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Errorf("walk = %v\nwant %v", seen, want)
	}
}

func TestAnUntitledConversationCompletesByItsID(t *testing.T) {
	m := refModel(t)
	got, _, ok := complete(&m, "#s-ga", 5)
	if !ok || got != "#s-gamma" {
		t.Errorf("got %q ok=%v", got, ok)
	}
}

// A reference to the conversation it is typed in resolves to "there is
// nothing to read", so offering it is offering a mistake.
func TestTheConversationYouAreInIsNotOffered(t *testing.T) {
	m := refModel(t)
	if _, _, ok := complete(&m, "#this", 5); ok {
		t.Error("the current conversation was offered")
	}
}

// Every one of these appears in ordinary prose and none should start a
// walk.
func TestAHashThatIsNotAReferenceIsLeftAlone(t *testing.T) {
	m := refModel(t)
	for _, tc := range []struct {
		text   string
		cursor int
	}{
		{"# heading", 9},
		{"see issue #42", 13},
		{"a#b", 3},
		{`#"the parser rewrite" `, 22},
		{"#the parser", 11},
	} {
		if _, _, ok := complete(&m, tc.text, tc.cursor); ok {
			t.Errorf("%q started a completion", tc.text)
		}
	}
}

// The relaxation this needed, and its limit.
func TestTheArrowIsStillACursorKeyInsideAWord(t *testing.T) {
	m := refModel(t)
	m.input.SetValue("check #then against it")
	m.input.SetCursorColumn(9)
	if m.cursorCompletable() {
		t.Error("the arrow completed from the middle of a word")
	}
	m.input.SetCursorColumn(11) // just before the space
	if !m.cursorCompletable() {
		t.Error("the arrow refused to complete at the end of a word")
	}
}

// "/" did not change. Its span is the whole box, so a splice and a
// substitution are the same thing there.
func TestACommandStillCompletesTheWholeBox(t *testing.T) {
	m := refModel(t)
	m.slashList = []client.SlashCommandInfo{{Name: "smart-agent"}, {Name: "skill"}}
	got, at, ok := complete(&m, "/sm", 3)
	if !ok || got != "/smart-agent" {
		t.Errorf("got %q ok=%v", got, ok)
	}
	if at != len("/smart-agent") {
		t.Errorf("cursor = %d", at)
	}
	// And not from the middle of the box, which is where a command is
	// not a command.
	if _, _, ok := complete(&m, "/sm x", 3); ok {
		t.Error("a command completed with text after it")
	}
}

// Runes, not bytes. A Korean prompt with a reference in it is the
// ordinary case here, and a byte offset into one lands mid-character.
func TestASplicePlacedByRunesSurvivesAKoreanPrompt(t *testing.T) {
	m := refModel(t)
	typed := "이 파일을 #the"
	got, at, ok := complete(&m, typed+" 와 비교해라", len([]rune(typed)))
	if !ok {
		t.Fatal("the arrow did not complete")
	}
	want := `이 파일을 #"the parser rewrite" 와 비교해라`
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if wantAt := len([]rune(`이 파일을 #"the parser rewrite"`)); at != wantAt {
		t.Errorf("cursor = %d, want %d", at, wantAt)
	}
}
