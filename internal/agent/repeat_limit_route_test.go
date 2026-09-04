package agent

import (
	"strings"
	"testing"
)

// "/repeat-limit" moves the ceiling, turns it off, or reports it, and
// every reply says what a repeat is: the notice that brings someone here
// was read as "one call three times", and it is not.
func TestRepeatLimitCommand(t *testing.T) {
	loop, sid := testLoop(t, "")
	run := func(text string) string {
		t.Helper()
		handled, err := loop.routeRepeatLimit(sid, text)
		if err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		if !handled {
			t.Fatalf("%s was not recognized", text)
		}
		return lastReply(t, loop, sid)
	}

	if got := run("/repeat-limit"); !strings.Contains(got, "repeat_limit: 3") {
		t.Errorf("bare = %q, want the default reported", got)
	}
	if got := run("/repeat-limit off"); !strings.Contains(got, "off") || loop.RepeatLimit() != 0 {
		t.Errorf("off = %q, limit %d", got, loop.RepeatLimit())
	}
	if got := run("/repeat-limit 8"); !strings.Contains(got, "8 steps in a row") || loop.RepeatLimit() != 8 {
		t.Errorf("8 = %q, limit %d", got, loop.RepeatLimit())
	}
	if !strings.Contains(run("/repeat-limit 8"), "Alternating between two reads counts") {
		t.Error("the reply does not say what a repeat is")
	}
	if got := run("/repeat-limit 999"); !strings.Contains(got, "usage") || loop.RepeatLimit() != 8 {
		t.Errorf("999 = %q, limit %d; want a usage line and no change", got, loop.RepeatLimit())
	}
	if got := run("/repeat-limit default"); loop.RepeatLimit() != 3 {
		t.Errorf("default = %q, limit %d", got, loop.RepeatLimit())
	}
	if handled, _ := loop.routeRepeatLimit(sid, "/repeat-limits"); handled {
		t.Error("/repeat-limits was taken as the command")
	}
}
