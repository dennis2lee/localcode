package tui

import (
	"encoding/json"
	"testing"

	"localcode/internal/events"
)

// frame decodes one event as the daemon's stream sends it, the way the
// client does, so the "stale" mark is read from the shape that arrives.
func frame(t *testing.T, payload string) events.Event {
	t.Helper()
	var ev events.Event
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		t.Fatalf("bad frame %s: %v", payload, err)
	}
	return ev
}

// A question the daemon serves stale is not put in front of the person.
//
// The daemon marks a replayed permission.request or input.request it is
// not waiting on and nothing in the log answers: one a stopped daemon
// left open. Shown, the permission one refused every message until it
// was answered, and the model one took the next message as its answer;
// both answers got the daemon's 404. A live one is still shown.
func TestAStaleQuestionIsNotShown(t *testing.T) {
	const (
		stalePerm = `{"seq":2,"type":"permission.request","data":{"id":"d1-p1","tool":"bash","description":"rm -rf build/","rule":"rm *","stale":true}}`
		staleAsk  = `{"seq":3,"type":"input.request","data":{"id":"d1-i1","question":"which one?","options":["a","b"],"stale":true}}`
		livePerm  = `{"seq":5,"type":"permission.request","data":{"id":"d2-p1","tool":"bash","description":"go test ./...","rule":"go *"}}`
		liveAsk   = `{"seq":6,"type":"input.request","data":{"id":"d2-i1","question":"which one?","options":["a","b"]}}`
	)

	t.Run("stale questions open nothing", func(t *testing.T) {
		m := newTestModel()
		m.applyEvent(frame(t, stalePerm))
		m.applyEvent(frame(t, staleAsk))
		if m.pending != nil || len(m.pendingQueue) != 0 {
			t.Errorf("a stale permission request is on screen: pending %+v, queue %d", m.pending, len(m.pendingQueue))
		}
		if m.asking != nil {
			t.Errorf("a stale model question took the box: %+v", m.asking)
		}
		// Enter sends the message as a prompt: no question refuses it,
		// and none takes it as an answer.
		m.input.SetValue("hello")
		next, cmd := handleEnter(m)
		if cmd == nil {
			t.Error("Enter sent nothing after a replay of stale questions")
		}
		if got := next.(Model).input.Value(); got != "" {
			t.Errorf("the prompt box still holds %q after Enter", got)
		}
	})

	t.Run("a live question after a stale one is shown", func(t *testing.T) {
		m := newTestModel()
		for _, f := range []string{stalePerm, staleAsk, livePerm, liveAsk} {
			m.applyEvent(frame(t, f))
		}
		if m.pending == nil || m.pending.id != "d2-p1" {
			t.Errorf("on screen is %+v, want the live d2-p1", m.pending)
		}
		if len(m.pendingQueue) != 0 {
			t.Errorf("%d requests queued behind the live one, want none", len(m.pendingQueue))
		}
		if m.asking == nil || m.asking.id != "d2-i1" {
			t.Errorf("the box is armed for %+v, want the live d2-i1", m.asking)
		}
	})
}
