package agent

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"localcode/internal/events"
)

// lastReply is the text of the last assistant message in a session.
func lastReply(t *testing.T, l *Loop, sessionID string) string {
	t.Helper()
	evs, err := l.Store.Events(sessionID, 0)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == events.TypeMessagePartEnd {
			text, _ := evs[i].Data["text"].(string)
			return text
		}
	}
	t.Fatal("no reply in the session")
	return ""
}

// "/update" is answered by the daemon through a hook, the way
// "/reset-mcp" is, and for the same reason: what it needs — which
// sessions are busy, whether this process may replace its own binary,
// how to come back — is all above this package.
func TestUpdateCommandGoesThroughTheHook(t *testing.T) {
	srv := httptest.NewServer(nil)
	defer srv.Close()
	loop, sid := testLoop(t, srv.URL)

	var gotSession string
	loop.SelfUpdate = func(sessionID string) (string, error) {
		gotSession = sessionID
		return "localcode 9.9.9 installed.", nil
	}

	handled, err := loop.routeUpdate(sid, "/update")
	if err != nil {
		t.Fatalf("/update: %v", err)
	}
	if !handled {
		t.Fatal("/update was not recognized as a command")
	}
	if gotSession != sid {
		t.Errorf("the hook was told session %q, want %q — it needs the caller to leave that session's own turn out of the busy check", gotSession, sid)
	}
	if got := lastReply(t, loop, sid); !strings.Contains(got, "9.9.9") {
		t.Errorf("reply = %q, want the hook's report", got)
	}
}

// A refusal reaches the person as a sentence rather than as a failed
// command. The daemon's message names what is running, and that is the
// useful part, so it is passed through rather than summarized.
func TestUpdateCommandPassesTheRefusalThrough(t *testing.T) {
	srv := httptest.NewServer(nil)
	defer srv.Close()
	loop, sid := testLoop(t, srv.URL)

	loop.SelfUpdate = func(string) (string, error) {
		return "", errors.New("2 background task(s) are still working: task-a, task-b")
	}

	if _, err := loop.routeUpdate(sid, "/update"); err != nil {
		t.Fatalf("/update: %v", err)
	}
	got := lastReply(t, loop, sid)
	if !strings.Contains(got, "task-a") {
		t.Errorf("reply = %q, want it to name what is running", got)
	}
}

// No hook is not a crash and not silence. A TUI attached over --server is
// looking at a daemon on another machine, which is not this conversation's
// to replace, and saying that is more use than "unknown command".
func TestUpdateCommandWithoutAHookSaysWhy(t *testing.T) {
	srv := httptest.NewServer(nil)
	defer srv.Close()
	loop, sid := testLoop(t, srv.URL)

	handled, err := loop.routeUpdate(sid, "/update")
	if err != nil {
		t.Fatalf("/update: %v", err)
	}
	if !handled {
		t.Fatal("/update should still be recognized without a hook wired")
	}
	if got := lastReply(t, loop, sid); !strings.Contains(got, "cannot install updates for itself") {
		t.Errorf("reply = %q, want it to say why", got)
	}
}

// Only the exact command. "/update the docs and push" is a prompt about
// updating something, and routing it to the installer would replace the
// program instead of doing what was asked.
func TestUpdateCommandIsNotAPrefix(t *testing.T) {
	srv := httptest.NewServer(nil)
	defer srv.Close()
	loop, sid := testLoop(t, srv.URL)
	loop.SelfUpdate = func(string) (string, error) {
		t.Error("the installer ran for a prompt that merely starts with /update")
		return "", nil
	}

	for _, text := range []string{"/update the docs and push", "/updates", "please /update"} {
		handled, err := loop.routeUpdate(sid, text)
		if err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		if handled {
			t.Errorf("%q was taken as the update command", text)
		}
	}
}
