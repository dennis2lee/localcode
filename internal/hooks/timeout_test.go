package hooks

import (
	"context"
	"strings"
	"testing"
	"time"
)

// What a hook that does not finish means.
//
// It used to mean nothing in particular: the kill arrived as
// "signal: killed" in a warning, indistinguishable from a script that ran
// and failed, and the tool went ahead. A pre_tool_use hook written to
// stop something dangerous was silently not stopping it, and there was no
// way to say otherwise and no way to give it longer.

// A hook can be given longer than the shared default.
func TestAHookCanBeGivenItsOwnTimeout(t *testing.T) {
	if got := (Hook{}).timeout(); got != defaultTimeout {
		t.Errorf("a hook with no timeout gets %s, want the %s default", got, defaultTimeout)
	}
	if got := (Hook{Timeout: 45}).timeout(); got != 45*time.Second {
		t.Errorf("timeout = %s, want 45s", got)
	}
	// Zero and negative both mean "the default" rather than "no time at
	// all", which is the reading that would turn a typo into a hook that
	// can never run.
	if got := (Hook{Timeout: -1}).timeout(); got != defaultTimeout {
		t.Errorf("a negative timeout gets %s, want the default", got)
	}
}

// The default is still to let the action through, and now it says why.
func TestATimedOutHookSaysItNeverDecided(t *testing.T) {
	start := time.Now()
	cfg := Config{"pre_tool_use": []Hook{{Command: "sleep 30", Timeout: 1}}}
	out := RunOutcome(context.Background(), cfg, "pre_tool_use", "", map[string]any{"tool_name": "bash"})
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("the hook ran for %s, so its own timeout was not applied", took)
	}
	if out.Blocked {
		t.Error("a timed-out hook blocked by default; that is a lockout waiting to happen")
	}
	if len(out.Warnings) != 1 {
		t.Fatalf("warnings = %v, want the one about not finishing", out.Warnings)
	}
	msg := out.Warnings[0].Error()
	for _, want := range []string{"did not finish", "never decided", "went ahead"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the warning does not say %q: %s", want, msg)
		}
	}
	// And not as an ordinary script failure, which is what it used to
	// look like.
	if strings.Contains(msg, "signal:") {
		t.Errorf("a timeout is still reported as a signal: %s", msg)
	}
}

// And a hook that says so blocks instead.
func TestAFailClosedHookBlocksWhenItCannotFinish(t *testing.T) {
	cfg := Config{"pre_tool_use": []Hook{{Command: "sleep 30", Timeout: 1, FailClosed: true}}}
	out := RunOutcome(context.Background(), cfg, "pre_tool_use", "", map[string]any{"tool_name": "bash"})
	if !out.Blocked {
		t.Fatal("a fail_closed hook that timed out let the action through")
	}
	for _, want := range []string{"did not finish", "fail_closed"} {
		if !strings.Contains(out.Reason, want) {
			t.Errorf("the reason does not say %q: %s", want, out.Reason)
		}
	}
}

// fail_closed covers not-finishing only. A hook that ran and exited
// nonzero has decided — the contract is exit 2 to block — and treating
// every bug in a script as a veto would make each one a lockout.
func TestFailClosedDoesNotTurnABrokenScriptIntoAVeto(t *testing.T) {
	cfg := Config{"pre_tool_use": []Hook{{Command: "exit 7", FailClosed: true}}}
	out := RunOutcome(context.Background(), cfg, "pre_tool_use", "", map[string]any{"tool_name": "bash"})
	if out.Blocked {
		t.Errorf("a script that exited 7 blocked under fail_closed: %s", out.Reason)
	}
	if len(out.Warnings) == 0 {
		t.Error("nothing was reported about a hook that failed")
	}
	// Exit 2 is still the way to block, fail_closed or not.
	cfg = Config{"pre_tool_use": []Hook{{Command: "echo no >&2; exit 2", FailClosed: true}}}
	if out := RunOutcome(context.Background(), cfg, "pre_tool_use", "", nil); !out.Blocked {
		t.Error("exit 2 no longer blocks")
	}
}

// A turn cancelled while a hook is running is not the hook timing out,
// and must not be reported as one — least of all as a block.
func TestACancelledTurnIsNotAHookTimingOut(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cfg := Config{"pre_tool_use": []Hook{{Command: "sleep 30", Timeout: 60, FailClosed: true}}}
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	out := RunOutcome(ctx, cfg, "pre_tool_use", "", map[string]any{"tool_name": "bash"})
	if out.Blocked {
		t.Errorf("stopping a turn was reported as a hook refusing it: %s", out.Reason)
	}
}
