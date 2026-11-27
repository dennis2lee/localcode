package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"localcode/internal/client"
	"localcode/internal/events"
)

// A turn's end is in the log before anything of the next turn. Clients
// read turn.done and turn.cancelled as the end of the turn before them in
// the log: they put away a question still held at that point, and the
// Web UI takes a stopped turn's permission request down. A turn that
// began between the registration being cleared and the end being written
// could ask first, and the end written after it took its live question
// off screen. So begin waits while an end is being written.
//
// Two begins, so that both waiting for one end is covered too: they are
// let go together, and only one of them may start a turn. A begin that
// read the registration before waiting and did not read it again after
// started two turns in one session.
func TestABeginWaitsForTheTurnBeforeToWriteItsEnd(t *testing.T) {
	ends := map[string]func(tr *turnTracker, write func()){
		"finishOrTake": func(tr *turnTracker, write func()) {
			if _, more := tr.finishOrTake("s1", write); more {
				t.Fatal("nothing was queued")
			}
		},
		"endWriting": func(tr *turnTracker, write func()) { tr.endWriting("s1", write) },
	}
	begins := map[string]func(tr *turnTracker, cancel context.CancelFunc) bool{
		"begin":          func(tr *turnTracker, cancel context.CancelFunc) bool { return tr.begin("s1", cancel) },
		"beginExclusive": func(tr *turnTracker, cancel context.CancelFunc) bool { return tr.beginExclusive("s1", cancel) },
	}
	for endName, end := range ends {
		for beginName, begin := range begins {
			t.Run(endName+"/"+beginName, func(t *testing.T) {
				tr := newTurnTracker()
				_, cancel := context.WithCancel(context.Background())
				defer cancel()
				if !tr.begin("s1", cancel) {
					t.Fatal("begin")
				}

				const n = 2
				began := make(chan bool, n)
				end(&tr, func() {
					for range n {
						go func() { began <- begin(&tr, cancel) }()
					}
					// Each begin either returns, which is the bug, or comes
					// to rest waiting on the end.
					for deadline := time.Now().Add(10 * time.Second); waitingForAnEnd() < n; time.Sleep(time.Millisecond) {
						select {
						case <-began:
							t.Fatalf("%s returned while the turn before was writing its end: the next turn's first event can come before that end in the log", beginName)
						default:
						}
						if time.Now().After(deadline) {
							t.Fatalf("%d of %d %s calls are waiting for the end after 10s", waitingForAnEnd(), n, beginName)
						}
					}
				})
				won := 0
				for range n {
					select {
					case ok := <-began:
						if ok {
							won++
						}
					case <-time.After(10 * time.Second):
						t.Fatalf("%s still waiting after the end was written", beginName)
					}
				}
				if won != 1 {
					t.Errorf("%d of %d %s calls that waited for one end started a turn, want 1", won, n, beginName)
				}
			})
		}
	}
}

// waitingForAnEnd counts the goroutines blocked in awaitEnd on an end
// being written: a receive on the ending channel, read from the stacks.
func waitingForAnEnd() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	waiting := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		header, _, _ := strings.Cut(g, "\n")
		if strings.Contains(header, "[chan receive") && strings.Contains(g, "(*turnTracker).awaitEnd(") {
			waiting++
		}
	}
	return waiting
}

// Nothing registers a turn between the registration being cleared and the
// session being marked as ending. Repeated, because it is a race: four
// begins spin against each end, and one that slipped in would show in
// running() during the write. It never fails correct code.
func TestNoTurnRegistersBeforeTheEndIsWritten(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < 3000; i++ {
		tr := newTurnTracker()
		if !tr.begin("s1", cancel) {
			t.Fatal("begin")
		}
		var won atomic.Bool
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for !won.Load() {
					if tr.begin("s1", cancel) {
						won.Store(true)
						return
					}
				}
			}()
		}
		var during []string
		tr.finishOrTake("s1", func() { during = tr.running() })
		wg.Wait()
		if len(during) > 0 {
			t.Fatalf("attempt %d: a turn was registered (%v) while the end of the one before was being written", i, during)
		}
	}
}

// A message turned away by inject while the end is being written is not
// lost: inject reports no turn, which sends the handler to begin, and
// begin starts the turn once the end is written.
func TestInjectReportsNoTurnWhileItsEndIsWritten(t *testing.T) {
	tr := newTurnTracker()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr.begin("s1", cancel)

	var injected bool
	tr.finishOrTake("s1", func() { injected = tr.inject("s1", "late", nil) })
	if injected {
		t.Fatal("inject accepted a message into a turn that had finished deciding to end; nobody would take it")
	}
	if text, ok := tr.takeOne("s1"); ok {
		t.Errorf("takeOne = %q: the message was queued for a turn that has ended", text)
	}
}

// Busy goes false after the end is written, so a session announced idle
// has a log whose last turn has ended: a window that opens it on seeing
// it idle does not read a turn still running.
func TestATurnIsAnnouncedIdleAfterItsEndIsWritten(t *testing.T) {
	for _, endName := range []string{"finishOrTake", "endWriting"} {
		t.Run(endName, func(t *testing.T) {
			tr := newTurnTracker()
			var mu sync.Mutex
			var order []string
			note := func(s string) {
				mu.Lock()
				order = append(order, s)
				mu.Unlock()
			}
			tr.onChange = func(_ string, busy bool) {
				if !busy {
					note("idle")
				}
			}
			_, cancel := context.WithCancel(context.Background())
			defer cancel()
			tr.begin("s1", cancel)
			write := func() { note("end written") }
			if endName == "finishOrTake" {
				tr.finishOrTake("s1", write)
			} else {
				tr.endWriting("s1", write)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(order) != 2 || order[0] != "end written" || order[1] != "idle" {
				t.Errorf("order = %v, want [end written idle]", order)
			}
		})
	}
}

// The same order through the send handler, on each of its three ways to
// end a turn: the model is done, the turn is stopped, the model request
// fails. The tracker's onChange is where the session goes idle; a message
// sent from there by another client starts the next turn, which asks a
// permission question. Its message.user has to come after the first
// turn's end in the log.
func TestTheNextTurnComesAfterTheEndOfTheOneBefore(t *testing.T) {
	for _, way := range []string{"done", "cancelled", "failed"} {
		t.Run(way, func(t *testing.T) { nextTurnAfterEnd(t, way) })
	}
}

func nextTurnAfterEnd(t *testing.T, way string) {
	writePath := filepath.Join(t.TempDir(), "out.txt")
	// A request whose last message is the person's asks for write_file,
	// which the broker gates; a request after a tool result answers in
	// text. For "failed", the first request is refused.
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 && way == "failed" {
			http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
			return
		}
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var last map[string]any
		if n := len(body.Messages); n > 0 {
			last = body.Messages[n-1]
		}
		role, _ := last["role"].(string)
		w.Header().Set("Content-Type", "text/event-stream")
		var chunks []string
		if role == "user" {
			inner, _ := json.Marshal(map[string]string{"path": writePath, "content": "hi"})
			args, _ := json.Marshal(string(inner))
			chunks = []string{
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"write_file","arguments":""}}]}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":` + string(args) + `}}]}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			}
		} else {
			chunks = []string{
				`{"choices":[{"delta":{"content":"done."}}]}`,
				`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			}
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	c := client.New(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sess, err := c.CreateSession(ctx, "general-purpose")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	logged := func() []events.Event {
		evs, _ := d.Loop.Store.Events(sess.ID, 0)
		return evs
	}
	requests := func() []string {
		var ids []string
		for _, ev := range logged() {
			if ev.Type == events.TypePermissionRequest {
				id, _ := ev.Data["id"].(string)
				ids = append(ids, id)
			}
		}
		return ids
	}
	waitFor := func(what string, ok func() bool) bool {
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if ok() {
				return true
			}
		}
		t.Errorf("timed out waiting for %s", what)
		return false
	}

	firstAsks := way != "failed"
	prev := d.turns.onChange
	var once sync.Once
	hooked := make(chan struct{})
	d.turns.onChange = func(id string, busy bool) {
		if prev != nil {
			prev(id, busy)
		}
		if busy || id != sess.ID {
			return
		}
		once.Do(func() {
			defer close(hooked)
			if _, err := c.SendMessage(ctx, sess.ID, "second"); err != nil {
				t.Errorf("second SendMessage: %v", err)
				return
			}
			want := 1
			if firstAsks {
				want = 2
			}
			waitFor("the next turn's permission request", func() bool { return len(requests()) >= want })
		})
	}

	if _, err := c.SendMessage(ctx, sess.ID, "first"); err != nil {
		t.Fatalf("first SendMessage: %v", err)
	}
	switch way {
	case "done":
		waitFor("the first turn's permission request", func() bool { return len(requests()) == 1 })
		d.Broker.Resolve(requests()[0], false, "")
	case "cancelled":
		waitFor("the first turn's permission request", func() bool { return len(requests()) == 1 })
		if err := c.CancelTurn(ctx, sess.ID); err != nil {
			t.Fatalf("CancelTurn: %v", err)
		}
	}
	select {
	case <-hooked:
	case <-ctx.Done():
		t.Fatal("the first turn never went idle")
	}

	endAt, secondAt := -1, -1
	var endType events.Type
	var order []string
	for i, ev := range logged() {
		text, _ := ev.Data["text"].(string)
		switch ev.Type {
		case events.TypeTurnDone, events.TypeTurnCancelled:
			if endAt < 0 {
				endAt, endType = i, ev.Type
			}
		case events.TypeUserMessage:
			if text == "second" {
				secondAt = i
			}
		case events.TypePermissionRequest, events.TypePermissionResolved:
		default:
			continue
		}
		order = append(order, fmt.Sprintf("%d:%s %s", ev.Seq, ev.Type, text))
	}
	if endAt < 0 || secondAt < 0 || secondAt < endAt {
		t.Errorf("the next turn's message (log index %d) is not after the first turn's end (index %d): an end written after the next turn's question takes that live question off screen\nlog:\n  %s",
			secondAt, endAt, strings.Join(order, "\n  "))
	}
	// Each way has its own end: a stopped turn's turn.cancelled drops the
	// clients' prompt queue, and a failed one must not.
	want := events.TypeTurnDone
	if way == "cancelled" {
		want = events.TypeTurnCancelled
	}
	if endType != want {
		t.Errorf("the first turn ended with %s, want %s", endType, want)
	}
	// Let the next turn go, and finish, before the model server closes.
	if ids := requests(); len(ids) > 0 {
		d.Broker.Resolve(ids[len(ids)-1], false, "")
	}
	waitFor("the next turn's end", func() bool {
		ends := 0
		for _, ev := range logged() {
			if ev.Type == events.TypeTurnDone || ev.Type == events.TypeTurnCancelled {
				ends++
			}
		}
		return ends >= 2
	})
}
