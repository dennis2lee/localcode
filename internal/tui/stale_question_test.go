package tui

import (
	"encoding/json"
	"testing"

	tea "charm.land/bubbletea/v2"

	"localcode/internal/client"
	"localcode/internal/events"
	"localcode/internal/session"
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

// A model question armed in one conversation does not follow a switch to
// another. It sat under the box in the conversation opened, and the next
// Enter there was sent as its answer, which the daemon refused because it
// was not asked in that session. Reached from the keyboard: the picker is
// open when the model asks, and the person picks another conversation.
func TestAModelQuestionDoesNotFollowASessionSwitch(t *testing.T) {
	const liveAsk = `{"seq":6,"type":"input.request","data":{"id":"d2-i1","question":"which one?","options":["a","b"]}}`
	m := newTestModel()
	updated, _ := m.Update(sessionsMsg{sessions: []session.Session{
		{ID: "s1", Title: "first", Agent: "general-purpose"},
		{ID: "s2", Title: "second", Agent: "general-purpose"},
	}})
	m = updated.(Model)
	if m.picker == nil {
		t.Fatal("precondition: the listing opened no picker")
	}
	updated, _ = m.handleServerEvent(eventMsg{gen: m.streamGen, ev: frame(t, liveAsk)})
	m = updated.(Model)
	if m.asking == nil {
		t.Fatal("precondition: the live question did not arm the box")
	}

	m = tapKey(t, m, tea.KeyDown) // onto s2
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("precondition: picking s2 opened nothing")
	}
	updated, _ = m.handleSessionSwitched(sessionSwitchedMsg{
		sessionID: "s2",
		agent:     "general-purpose",
		gen:       m.streamGen,
		events:    make(chan events.Event),
	})
	m = updated.(Model)
	if m.sessionID != "s2" {
		t.Fatalf("precondition: the switch landed on %q, not s2", m.sessionID)
	}
	if m.asking != nil {
		t.Errorf("after switching to s2 the box is still armed for s1's question %q", m.asking.id)
	}
	m.input.SetValue("hello")
	next, _ := handleEnter(m)
	if !next.(Model).waiting {
		t.Error("Enter in s2 did not send the message as a prompt: it went to s1's question")
	}
}

// A client that saw a question live puts it away when the daemon it
// resumes on says nobody is waiting on it. The daemon that asked stopped,
// and the client was only watching the turn, so the lost-turn check does
// not run and the resumed stream carries no copy of the question. The
// daemon sends a resolution with no seq instead (staleResolutions in
// internal/daemon); these are its frames as the client decodes them.
func TestAStaleResolutionPutsAwayAHeldQuestion(t *testing.T) {
	const (
		user      = `{"seq":1,"type":"message.user","data":{"text":"pick one"}}`
		livePerm  = `{"seq":2,"type":"permission.request","data":{"id":"d1-p1","tool":"bash","description":"rm -rf build/","rule":"rm *"}}`
		liveAsk   = `{"seq":3,"type":"input.request","data":{"id":"d1-i1","question":"which one?","options":["a","b"]}}`
		otherPerm = `{"seq":4,"type":"permission.request","data":{"id":"d1-p2","tool":"bash","description":"go test ./...","rule":"go *"}}`
		stalePerm = `{"seq":0,"type":"permission.resolved","data":{"id":"d1-p1","allow":false,"unanswered":true,"stale":true}}`
		staleAsk  = `{"seq":0,"type":"input.resolved","data":{"id":"d1-i1","cancelled":true,"stale":true}}`
		staleNone = `{"seq":0,"type":"permission.resolved","data":{"id":"d0-p9","allow":false,"unanswered":true,"stale":true}}`
	)
	m := newTestModel()
	for _, f := range []string{user, livePerm, liveAsk, otherPerm} {
		upd, _ := m.handleServerEvent(eventMsg{gen: m.streamGen, ev: frame(t, f)})
		m = upd.(Model)
	}
	upd, _ := m.handleServerEvent(eventMsg{gen: m.streamGen, ev: events.Event{Type: client.TypeReconnected}})
	m = upd.(Model)
	for _, f := range []string{staleNone, stalePerm, staleAsk} {
		upd, _ := m.handleServerEvent(eventMsg{gen: m.streamGen, ev: frame(t, f)})
		m = upd.(Model)
	}
	if m.asking != nil {
		t.Errorf("the box is still armed for %q, which the daemon said nobody is waiting on", m.asking.id)
	}
	if m.pending == nil || m.pending.id != "d1-p2" || len(m.pendingQueue) != 0 {
		t.Errorf("on screen is %+v with %d queued, want d1-p2 alone: d1-p1 put away and d1-p2 left", m.pending, len(m.pendingQueue))
	}
	if m.lastSeq != 4 {
		t.Errorf("the resume point moved to %d on resolutions that carry no seq, want 4", m.lastSeq)
	}
}
