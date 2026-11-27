package daemon

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"localcode/internal/client"
	"localcode/internal/events"
)

// The 202 for a message carries the log's last seq at the moment the
// daemon took it, and both clients decide by it whether a stop dropped
// the message (docs/API.md): a stop that drops it has a larger seq, and a
// stop written before it was taken has one at or below it.

func textModel(t *testing.T) *httptest.Server {
	t.Helper()
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done.\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(model.Close)
	return model
}

func TestAMessageHandedToARunningTurnIsTakenBeforeTheStopThatDropsIt(t *testing.T) {
	d := newTestDaemon(t, textModel(t).URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	c := client.New(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess, err := c.CreateSession(ctx, "general-purpose")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// A turn running, of another client's.
	_, stop := context.WithCancel(context.Background())
	defer stop()
	if !d.turns.begin(sess.ID, stop) {
		t.Fatal("begin")
	}
	if _, err := d.Loop.Store.Append(sess.ID, events.TypeUserMessage, map[string]any{"text": "first"}); err != nil {
		t.Fatal(err)
	}
	last, err := d.Loop.Store.LastSeq(sess.ID)
	if err != nil || last == 0 {
		t.Fatalf("precondition: LastSeq = %d, %v", last, err)
	}

	got, err := c.SendMessage(ctx, sess.ID, "and also")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got != (client.Receipt{Status: "injected", Seq: last, HasSeq: true}) {
		t.Fatalf("receipt %+v, want injected at the log's last seq %d", got, last)
	}

	var end events.Event
	d.turns.endWriting(sess.ID, func() {
		end, err = d.Loop.Store.Append(sess.ID, events.TypeTurnCancelled, map[string]any{})
	})
	if err != nil {
		t.Fatal(err)
	}
	if end.Seq <= got.Seq {
		t.Errorf("the stop that dropped the message is at seq %d, and the message was taken at %d: a client keeps it drawn as sent", end.Seq, got.Seq)
	}
}

// A message sent while the stopped turn writes its end waits for the end
// in begin, and starts a turn of its own after it.
func TestAMessageThatWaitedForAStopIsTakenAfterIt(t *testing.T) {
	d := newTestDaemon(t, textModel(t).URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	c := client.New(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sess, err := c.CreateSession(ctx, "general-purpose")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	_, stop := context.WithCancel(context.Background())
	defer stop()
	if !d.turns.begin(sess.ID, stop) {
		t.Fatal("begin")
	}
	type sent struct {
		r   client.Receipt
		err error
	}
	answered := make(chan sent, 1)
	var end events.Event
	d.turns.endWriting(sess.ID, func() {
		go func() {
			r, err := c.SendMessage(ctx, sess.ID, "next")
			answered <- sent{r, err}
		}()
		for deadline := time.Now().Add(10 * time.Second); waitingForAnEnd() < 1; time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Error("the message never came to wait for the end")
				break
			}
		}
		end, _ = d.Loop.Store.Append(sess.ID, events.TypeTurnCancelled, map[string]any{})
	})

	var got sent
	select {
	case got = <-answered:
	case <-ctx.Done():
		t.Fatal("no answer to the message")
	}
	if got.err != nil {
		t.Fatalf("SendMessage: %v", got.err)
	}
	if got.r.Status != "accepted" || !got.r.HasSeq {
		t.Fatalf("receipt %+v, want accepted with a seq", got.r)
	}
	if got.r.Seq < end.Seq {
		t.Errorf("the message was taken at seq %d, below the stop it waited for at %d: a client marks it as never sent", got.r.Seq, end.Seq)
	}

	// Its own message.user comes after the receipt; the turn's end after
	// that, which is also what this test waits for before it stops.
	var userAt uint64
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		evs, _ := d.Loop.Store.Events(sess.ID, end.Seq)
		done := false
		for _, ev := range evs {
			if text, _ := ev.Data["text"].(string); ev.Type == events.TypeUserMessage && text == "next" {
				userAt = ev.Seq
			}
			if ev.Type == events.TypeTurnDone {
				done = true
			}
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the message's turn never ended")
		}
	}
	if userAt <= got.r.Seq {
		t.Errorf("the message's own message.user is at seq %d, at or below the receipt's %d: a client takes it for an older message", userAt, got.r.Seq)
	}
}

// A stop can land after the turn read its context and before it ends:
// turnTracker.cancel drops the queue, and the end has to say a stop did.
// finishTurn is the step the turn's goroutine takes there.
func TestAStopAsATurnEndsEndsItAsCancelled(t *testing.T) {
	for _, stopped := range []bool{true, false} {
		t.Run(fmt.Sprintf("stopped=%v", stopped), func(t *testing.T) {
			d := newTestDaemon(t, textModel(t).URL)
			srv := httptest.NewServer(d.Handler())
			defer srv.Close()
			c := client.New(srv.URL)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			sess, err := c.CreateSession(ctx, "general-purpose")
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}

			turnCtx, stop := context.WithCancel(context.Background())
			defer stop()
			if !d.turns.begin(sess.ID, stop) {
				t.Fatal("begin")
			}
			var r client.Receipt
			want := events.TypeTurnDone
			if stopped {
				want = events.TypeTurnCancelled
				if r, err = c.SendMessage(ctx, sess.ID, "and also"); err != nil || r.Status != "injected" || !r.HasSeq {
					t.Fatalf("precondition: injected with a seq, got %+v %v", r, err)
				}
				// The turn's last step is done and its context read clean
				// when the stop lands.
				if !d.turns.cancel(sess.ID) {
					t.Fatal("precondition: the stop was honoured")
				}
			}
			if _, more := d.finishTurn(sess.ID, turnCtx); more {
				t.Fatal("the turn went on with a queued message")
			}

			evs, _ := d.Loop.Store.Events(sess.ID, 0)
			last := evs[len(evs)-1]
			if last.Type != want || last.Seq <= r.Seq {
				t.Errorf("the turn ended with %s at seq %d, a message was taken at %d: want %s after it, or no client marks a dropped message as not sent",
					last.Type, last.Seq, r.Seq, want)
			}
		})
	}
}

// The stop's cancel runs under the tracker's lock. finishTurn takes that
// lock to clear the registration, so it sees either the stop, as a
// cancelled context, or no stop at all: cancel then finds nothing running
// and answers false.
func TestAStopCancelsTheTurnUnderTheTrackersLock(t *testing.T) {
	tr := newTurnTracker()
	held := false
	if !tr.begin("s1", func() {
		held = !tr.mu.TryLock()
		if !held {
			tr.mu.Unlock()
		}
	}) {
		t.Fatal("begin")
	}
	if !tr.cancel("s1") {
		t.Fatal("precondition: the stop was honoured")
	}
	if !held {
		t.Error("the turn's context was cancelled after the lock was released: a turn that ends in between writes turn.done for a stop that dropped its queue")
	}
}

// The same, as a race: the end says a stop when one was honoured, and
// only then. It never fails correct code.
func TestAStopRacingATurnsEndIsWhatTheEndSays(t *testing.T) {
	d := newTestDaemon(t, textModel(t).URL)
	sess, err := d.Loop.Store.CreateSession("race", "", "general-purpose", true)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 300 {
		turnCtx, stop := context.WithCancel(context.Background())
		if !d.turns.begin(sess.ID, stop) {
			t.Fatal("begin")
		}
		honoured := make(chan bool, 1)
		go func() { honoured <- d.turns.cancel(sess.ID) }()
		if _, more := d.finishTurn(sess.ID, turnCtx); more {
			t.Fatal("nothing was queued")
		}
		stopped := <-honoured
		stop()

		evs, _ := d.Loop.Store.Events(sess.ID, 0)
		last := evs[len(evs)-1]
		if stopped && last.Type != events.TypeTurnCancelled {
			t.Fatalf("round %d: the stop was honoured and the turn ended with %s", i, last.Type)
		}
		if !stopped && last.Type != events.TypeTurnDone {
			t.Fatalf("round %d: no stop was honoured and the turn ended with %s", i, last.Type)
		}
	}
}
