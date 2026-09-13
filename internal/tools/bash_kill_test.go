package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A command killed for running too long says so even when the operating
// system hands back an ordinary exit status.
//
// This is the half that could not be seen from Unix. There a process
// localcode kills is killed by a signal, so exitStatus answers "no status
// chosen" and the deadline was discovered on the way through that branch.
// Windows exits a killed process with a code like any other, so the same
// command came back as "exited with status 1" — an ordinary failure, which
// is what a model retries. bash.go asks the context before it reads the
// code now, and this test pins that ordering without needing a platform
// where the codes differ: it hands killNotice a context that is already
// past its deadline together with a perfectly ordinary exit error, which
// is exactly the pair Windows produces.
func TestATimeoutIsNamedEvenWhenTheProcessChoseAStatus(t *testing.T) {
	timedOut, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	// What Windows hands back for a process that was killed: a real status,
	// not a signal.
	ordinary := &exec.ExitError{ProcessState: nil}
	var asExit *exec.ExitError
	if !errors.As(error(ordinary), &asExit) {
		t.Fatal("the fixture is not an *exec.ExitError")
	}

	got := killNotice(timedOut, 100*time.Millisecond, ordinary)
	if !strings.Contains(got, "timeout") {
		t.Errorf("killNotice with an expired deadline = %q, want it to name the timeout", got)
	}

	cancelledCtx, stop := context.WithCancel(context.Background())
	stop()
	if got := killNotice(cancelledCtx, time.Minute, ordinary); !strings.Contains(got, "cancelled") {
		t.Errorf("killNotice with a cancelled context = %q, want it to say cancelled", got)
	}

	// And a clean context keeps the raw error, because then the error
	// really is all that is known.
	if got := killNotice(context.Background(), time.Minute, ordinary); strings.Contains(got, "timeout") || strings.Contains(got, "cancelled") {
		t.Errorf("killNotice with a live context = %q, want the raw error", got)
	}
}

// And the whole tool, not just the notice: a timed-out command reports the
// timeout rather than whatever status the platform attached to the kill.
func TestTheBashToolNamesATimeoutRatherThanAnExitStatus(t *testing.T) {
	input, _ := json.Marshal(map[string]string{"command": portableSleepCommand()})
	res := Bash{Timeout: 150 * time.Millisecond}.Execute(context.Background(), input)
	if !res.IsError {
		t.Fatal("a command killed at its deadline should be an error")
	}
	if !strings.Contains(res.Content, "timeout") {
		t.Errorf("content = %q, want it to name the timeout", res.Content)
	}
	if strings.Contains(res.Content, "exited with status") {
		t.Errorf("content = %q, want the timeout rather than an exit status", res.Content)
	}
}

// portableSleepCommand waits longer than any timeout these tests set, in a
// way both a POSIX shell and Windows' own fallback understand.
//
// "sleep 5" is a Unix utility. Git for Windows ships one, so it works when
// internal/shell finds bash, and vanishes under the cmd /c fallback — where
// the command would fail instantly and the test would be measuring a
// missing binary rather than a deadline.
func portableSleepCommand() string {
	return "sleep 5 || ping -n 6 127.0.0.1 >NUL"
}

// Every combination of "what the context says" and "what the OS said",
// including the two only Windows produces.
//
// This is the test the ordering needed and could not have from Unix. A
// killed process there is killed by a signal, so exited is false and the
// kill is visible either way; the case that broke — an expired context
// beside a real exit status — cannot be produced by running a real command
// on this machine. As a function of its two inputs it is reachable from
// anywhere.
func TestAKilledCommandNeverCountsAsOneThatChoseItsStatus(t *testing.T) {
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	cancelled, stop := context.WithCancel(context.Background())
	stop()

	for _, c := range []struct {
		name   string
		ctxErr error
		exited bool
		chose  bool
	}{
		{"ran and failed on its own", nil, true, true},
		{"could not start at all", nil, false, false},
		// Unix: killed, so no status came back.
		{"killed at the deadline, Unix", expired.Err(), false, false},
		{"cancelled, Unix", cancelled.Err(), false, false},
		// Windows: killed, and the OS attached a status anyway. These two
		// are the whole reason this is a function.
		{"killed at the deadline, Windows", expired.Err(), true, false},
		{"cancelled, Windows", cancelled.Err(), true, false},
	} {
		if got := commandChoseItsStatus(c.ctxErr, c.exited); got != c.chose {
			t.Errorf("%s: commandChoseItsStatus(%v, %v) = %v, want %v — a command localcode killed must never be reported as one that chose to fail",
				c.name, c.ctxErr, c.exited, got, c.chose)
		}
	}
}
