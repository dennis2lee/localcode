package agent

import (
	"strings"
	"testing"

	"localcode/internal/session"
)

// The four commands TestEveryCommandIsNamedInATest found with nothing
// exercising them.
//
// Each shares a route with a sibling that was tested — skip-tools with
// skip-all, write-outside with read-outside — which is exactly why they
// were missed: the route was covered, so nothing looked absent. What was
// not covered is that each name reaches the right half of that shared
// route, which is the only thing a wrong wiring would change.

// "/permission-skip-tools" sets its own switch and not the blanket one
// beside it, and says which boundary it leaves standing.
func TestSkipToolsIsNotTheBlanketSwitch(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	out := replyTo(t, loop, sid, "/permission-skip-tools on")
	if !strings.Contains(out, "skip_tools: on") {
		t.Errorf("said %q, want skip_tools on", out)
	}
	if !loop.Permissions.OnFor(sid, session.SwitchSkipTools) {
		t.Error("skip_tools did not turn on")
	}
	// The distinction is the whole reason this command exists separately.
	if loop.Permissions.OnFor(sid, session.SwitchSkipAll) {
		t.Error("skip_tools turned the blanket switch on too")
	}
	if !strings.Contains(out, "/read-outside") || !strings.Contains(out, "/write-outside") {
		t.Errorf("said %q, want it to name the boundary it leaves standing", out)
	}

	if out := replyTo(t, loop, sid, "/permission-skip-tools off"); !strings.Contains(out, "skip_tools: off") {
		t.Errorf("said %q, want skip_tools off", out)
	}
	if loop.Permissions.OnFor(sid, session.SwitchSkipTools) {
		t.Error("skip_tools did not turn off")
	}
}

// "/write-outside" sets the writing half and leaves the reading half
// alone. They are separate because they are not the same risk.
func TestWriteOutsideIsNotTheReadingHalf(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	out := replyTo(t, loop, sid, "/write-outside on")
	if !strings.Contains(out, "write_outside: on") {
		t.Errorf("said %q, want write_outside on", out)
	}
	if !loop.Permissions.OnFor(sid, session.SwitchWriteOutside) {
		t.Error("write_outside did not turn on")
	}
	if loop.Permissions.OnFor(sid, session.SwitchReadOutside) {
		t.Error("write-outside turned reading outside on as well")
	}

	// mem-clear is the retraction that is not a setting change: the
	// approved directories go, the switch stays where it was. This build
	// has no broker holding any, and says so rather than reporting a
	// forget it did not perform.
	out = replyTo(t, loop, sid, "/write-outside mem-clear")
	if !strings.Contains(out, "nothing remembered to clear") {
		t.Errorf("mem-clear said %q, want it to say there was nothing to forget", out)
	}
	if !loop.Permissions.OnFor(sid, session.SwitchWriteOutside) {
		t.Error("mem-clear turned the switch off, and it should only forget directories")
	}
}

// "/show-scheduled-task" says so when nothing is booked, rather than
// answering with an empty list somebody would read as a failure.
func TestShowScheduledTaskSaysWhenNothingIsBooked(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	out := replyTo(t, loop, sid, "/show-scheduled-task")
	// Either answer is correct depending on whether this build wired a
	// scheduler; what must not happen is the text reaching a model.
	if !strings.Contains(out, "no scheduled tasks") && !strings.Contains(out, "no scheduler") {
		t.Errorf("said %q, want it answered locally", out)
	}
	if strings.Contains(out, "no scheduled tasks") && !strings.Contains(out, "/schedule") {
		t.Errorf("said %q, want it to say how to book one", out)
	}
}

// "/model-invocable on" turns the capability on and says that on its own
// it opens nothing — the commands the model may run are named elsewhere.
//
// The sentence matters more than the switch. Somebody turning this on
// and seeing only "on" would reasonably believe the model can now run
// their commands, and until something opts in it cannot.
func TestModelInvocableOpensNothingByItself(t *testing.T) {
	loop := newSmartLoop(t, "http://127.0.0.1:1")
	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	out := replyTo(t, loop, sid, "/model-invocable on")
	if !strings.Contains(out, "model_invocable: on") {
		t.Errorf("said %q, want model_invocable on", out)
	}
	if !loop.ModelInvocableEnabled() {
		t.Error("model_invocable did not turn on")
	}
	if !strings.Contains(out, "nothing is opted in yet") {
		t.Errorf("said %q, want it to say the switch alone opens nothing", out)
	}

	if out := replyTo(t, loop, sid, "/model-invocable off"); !strings.Contains(out, "model_invocable: off") {
		t.Errorf("said %q, want model_invocable off", out)
	}
	if loop.ModelInvocableEnabled() {
		t.Error("model_invocable did not turn off")
	}

	if out := replyTo(t, loop, sid, "/model-invocable sideways"); !strings.Contains(out, "usage:") {
		t.Errorf("a bad argument said %q, want the usage line", out)
	}
}
