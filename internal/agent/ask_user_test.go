package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"localcode/internal/events"
)

func askCall(t *testing.T, loop *Loop, sid, body string) (string, bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(WithSessionID(context.Background(), sid))
	defer cancel()

	done := make(chan string, 1)
	go func() {
		res := NewAskUserTool(loop).Execute(ctx, json.RawMessage(body))
		if res.IsError {
			done <- "ERROR: " + res.Content
			return
		}
		done <- res.Content
	}()

	// Wait for the question to reach the session, then answer it.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-done:
			return got, false
		case <-deadline:
			t.Fatal("the tool never asked and never returned")
		default:
		}
		evs, err := loop.Store.Events(sid, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs {
			if ev.Type != events.TypeInputRequest {
				continue
			}
			id, _ := ev.Data["id"].(string)
			if loop.Input.Resolve(id, "the second one") {
				return <-done, true
			}
		}
		time.Sleep(time.Millisecond)
	}
}

// The question reaches the session, the answer reaches the model, and
// the turn never ended in between.
func TestAskUserCarriesTheAnswerBack(t *testing.T) {
	loop, sid := testLoop(t, "")
	loop.Input = NewInputBroker(loop.Store)

	got, asked := askCall(t, loop, sid, `{"question":"Which store?","options":["Postgres","SQLite"]}`)
	if !asked {
		t.Fatalf("no question was asked: %s", got)
	}
	if !strings.Contains(got, "the second one") {
		t.Errorf("result = %q, want the answer", got)
	}

	evs, _ := loop.Store.Events(sid, 0)
	var sawRequest, sawResolved bool
	for _, ev := range evs {
		switch ev.Type {
		case events.TypeInputRequest:
			sawRequest = true
			if q, _ := ev.Data["question"].(string); q != "Which store?" {
				t.Errorf("question = %q", q)
			}
			if opts, _ := ev.Data["options"].([]any); len(opts) != 2 {
				t.Errorf("options = %v", opts)
			}
		case events.TypeInputResolved:
			sawResolved = true
		}
	}
	if !sawRequest || !sawResolved {
		t.Errorf("events: request=%v resolved=%v", sawRequest, sawResolved)
	}
}

// One question per turn. A model given an unlimited way to ask uses it,
// and a session that stops three times is worse than one that guessed.
func TestAskUserIsOncePerTurn(t *testing.T) {
	loop, sid := testLoop(t, "")
	loop.Input = NewInputBroker(loop.Store)

	if _, asked := askCall(t, loop, sid, `{"question":"a?","options":["x","y"]}`); !asked {
		t.Fatal("the first question was not asked")
	}
	ctx := WithSessionID(t.Context(), sid)
	res := NewAskUserTool(loop).Execute(ctx, json.RawMessage(`{"question":"b?","options":["x","y"]}`))
	if !res.IsError || !strings.Contains(res.Content, "already asked once this turn") {
		t.Errorf("second question = %+v, want a refusal", res)
	}

	// The budget is per turn, so the next turn may ask again.
	loop.releaseAsk(sid)
	if _, asked := askCall(t, loop, sid, `{"question":"c?","options":["x","y"]}`); !asked {
		t.Error("a new turn could not ask")
	}
}

// The shapes that make a question useless are refused before anybody is
// interrupted by them.
func TestAskUserRefusesUselessQuestions(t *testing.T) {
	loop, sid := testLoop(t, "")
	loop.Input = NewInputBroker(loop.Store)
	ctx := WithSessionID(t.Context(), sid)

	for _, tc := range []struct{ name, body, want string }{
		{"one option", `{"question":"a?","options":["only"]}`, "at least two options"},
		{"five options", `{"question":"a?","options":["1","2","3","4","5"]}`, "too many"},
		{"no question", `{"question":"   ","options":["a","b"]}`, "question is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := NewAskUserTool(loop).Execute(ctx, json.RawMessage(tc.body))
			if !res.IsError || !strings.Contains(res.Content, tc.want) {
				t.Errorf("result = %+v, want a refusal naming %q", res, tc.want)
			}
		})
	}
}

// A turn that ends before anybody answers gets an error rather than
// hanging, and the question comes off screen.
func TestAskUserGivesUpWhenTheTurnEnds(t *testing.T) {
	loop, sid := testLoop(t, "")
	loop.Input = NewInputBroker(loop.Store)

	ctx, cancel := context.WithCancel(WithSessionID(context.Background(), sid))
	done := make(chan string, 1)
	go func() {
		res := NewAskUserTool(loop).Execute(ctx, json.RawMessage(`{"question":"a?","options":["x","y"]}`))
		done <- res.Content
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case got := <-done:
		if !strings.Contains(got, "not answered before the turn ended") {
			t.Errorf("result = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the tool hung after its turn was cancelled")
	}
}

// Nobody at the keyboard, no tool. A scheduled run, a pipe and a
// delegated sub-agent are all cases where it could only block.
func TestAskUserIsHiddenWhereNobodyCanAnswer(t *testing.T) {
	loop, _ := testLoop(t, "")
	loop.SetSmartAgentEnabled(true)

	if loop.Input = NewInputBroker(loop.Store); loop.hiddenTools(t.Context())[askUserToolName] {
		t.Error("hidden in an ordinary attended session")
	}
	loop.Input = nil
	if !loop.hiddenTools(t.Context())[askUserToolName] {
		t.Error("offered with no broker to ask through")
	}
	loop.Input = NewInputBroker(loop.Store)
	loop.SetSmartAgentEnabled(false)
	if !loop.hiddenTools(t.Context())[askUserToolName] {
		t.Error("offered with Smart Agent off")
	}
}
