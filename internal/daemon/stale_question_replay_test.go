package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"localcode/internal/agent"
	"localcode/internal/events"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// sseReader reads one session's event stream frame by frame.
type sseReader struct {
	t       *testing.T
	scanner *bufio.Scanner
}

// openStream opens the event stream of session id on srv. The stream is
// closed when the test ends.
func openStream(t *testing.T, srv *httptest.Server, id string) *sseReader {
	t.Helper()
	prev := heartbeatInterval
	heartbeatInterval = 30 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = prev })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/sessions/"+id+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream of %s: %v", id, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open stream of %s: %s", id, resp.Status)
	}
	return &sseReader{t: t, scanner: bufio.NewScanner(resp.Body)}
}

// next returns the next event, or false at a heartbeat. The backlog is
// written before the first heartbeat, so reading to one reads all of it.
func (r *sseReader) next() (events.Event, bool) {
	r.t.Helper()
	for r.scanner.Scan() {
		line := r.scanner.Text()
		if strings.HasPrefix(line, ":") {
			return events.Event{}, false
		}
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev events.Event
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			r.t.Fatalf("bad frame %q: %v", payload, err)
		}
		return ev, true
	}
	r.t.Fatalf("the stream ended: %v", r.scanner.Err())
	return events.Event{}, false
}

// backlog reads the stream up to its first heartbeat.
func (r *sseReader) backlog() []events.Event {
	r.t.Helper()
	var out []events.Event
	for {
		ev, ok := r.next()
		if !ok {
			return out
		}
		out = append(out, ev)
	}
}

// questionsIn returns, for each permission.request and input.request in
// evs, in order, its id and whether it came marked stale.
func questionsIn(evs []events.Event) []string {
	var out []string
	for _, ev := range evs {
		if ev.Type != events.TypePermissionRequest && ev.Type != events.TypeInputRequest {
			continue
		}
		id, _ := ev.Data["id"].(string)
		if stale, _ := ev.Data["stale"].(bool); stale {
			id += " stale"
		}
		out = append(out, string(ev.Type)+" "+id)
	}
	return out
}

func question(seq uint64, typ events.Type, id string) events.Event {
	data := map[string]any{"id": id}
	if typ == events.TypePermissionRequest {
		data["tool"], data["description"] = "bash", "rm -rf build/"
	} else {
		data["question"] = "which one?"
	}
	return events.Event{Seq: seq, Type: typ, Timestamp: time.Unix(0, 0).UTC(), Data: data}
}

func answer(seq uint64, typ events.Type, id string) events.Event {
	return events.Event{Seq: seq, Type: typ, Timestamp: time.Unix(0, 0).UTC(), Data: map[string]any{"id": id, "answer": "a"}}
}

// A question a stopped daemon left open is served marked stale, and an
// answered one is served as it is in the log.
//
// Nothing resolves the open one: the daemon that would have is gone, and
// this one answers it 404. Both clients rebuild open questions from what
// this stream replays, so unmarked it was put in front of the person on
// every open of the conversation. Marked, both skip it.
func TestAQuestionNoDaemonIsAskingIsServedStale(t *testing.T) {
	dir := t.TempDir()
	const id = "s-left-open"
	writeSessionLog(t, dir, id, []events.Event{
		logEvent(1, events.TypeUserMessage, "clean up"),
		question(2, events.TypePermissionRequest, "d1-p1"),
		answer(3, events.TypePermissionResolved, "d1-p1"),
		question(4, events.TypeInputRequest, "d1-i1"),
		answer(5, events.TypeInputResolved, "d1-i1"),
		// The daemon stopped here, asking both.
		question(6, events.TypePermissionRequest, "d1-p2"),
		question(7, events.TypeInputRequest, "d1-i2"),
		// A log from before ids were per daemon: the next daemon's first
		// question had the same id as the first one asked here, was
		// answered, and then that daemon stopped asking another.
		question(8, events.TypePermissionRequest, "p1"),
		answer(9, events.TypePermissionResolved, "p1"),
		question(10, events.TypePermissionRequest, "p1"),
		// And the other order: one left open, then the same id asked
		// again by the next daemon and answered. The answer is the second
		// one's.
		question(11, events.TypePermissionRequest, "p2"),
		question(12, events.TypePermissionRequest, "p2"),
		answer(13, events.TypePermissionResolved, "p2"),
		// Two such daemons writing the log at once, each asking p3 and
		// each answered: two answers, one for each request.
		question(13, events.TypePermissionRequest, "p3"),
		question(13, events.TypePermissionRequest, "p3"),
		answer(14, events.TypePermissionResolved, "p3"),
		answer(14, events.TypePermissionResolved, "p3"),
		// A resolution for a permission does not answer a model question
		// that happens to carry the same id.
		question(14, events.TypeInputRequest, "x1"),
		answer(15, events.TypePermissionResolved, "x1"),
	})
	store, _, err := session.LoadAllFromDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	d := newTestDaemonOver(t, store, "http://127.0.0.1:1")
	srv := httptest.NewServer(d.Handler())
	t.Cleanup(srv.Close)

	got := questionsIn(openStream(t, srv, id).backlog())
	want := []string{
		"permission.request d1-p1",
		"input.request d1-i1",
		"permission.request d1-p2 stale",
		"input.request d1-i2 stale",
		"permission.request p1",
		"permission.request p1 stale",
		"permission.request p2 stale",
		"permission.request p2",
		"permission.request p3",
		"permission.request p3",
		"input.request x1 stale",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("questions served:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// The mark is on the copy sent, not on the record.
	evs, err := store.Events(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if _, ok := ev.Data["stale"]; ok {
			t.Errorf("seq %d in the store carries %q: the mark was written into the log's own event", ev.Seq, "stale")
		}
	}
}

// waitForRequest waits until session id's log holds a request of type typ
// and returns its id.
func waitForRequest(t *testing.T, store *session.Store, id string, typ events.Type) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		evs, _ := store.Events(id, 0)
		for _, ev := range evs {
			if ev.Type == typ {
				got, _ := ev.Data["id"].(string)
				return got
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %s was never asked a %s", id, typ)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A question this daemon is waiting on is served as asked: to the task
// that asked it, to the conversation it is mirrored into, and to that
// conversation's model question. A fork of the conversation copies the
// mirrored request and cannot answer it, so the fork is served it stale.
func TestALiveQuestionIsServedAsAsked(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	store := d.Loop.Store
	if _, err := store.CreateSession("conv", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSession("task", "conv", "general-purpose", false); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(d.Handler())
	t.Cleanup(srv.Close)

	permDone := make(chan struct{})
	go func() {
		defer close(permDone)
		d.Broker.Func()(agent.WithSessionID(context.Background(), "task"),
			tools.Ask{Tool: "bash", Subject: "go test ./...", Description: "go test ./..."})
	}()
	permID := waitForRequest(t, store, "conv", events.TypePermissionRequest)
	askCtx, cancelAsk := context.WithCancel(context.Background())
	askDone := make(chan struct{})
	go func() {
		defer close(askDone)
		d.Loop.Input.Ask(askCtx, "conv", "which one?", []string{"a", "b"})
	}()
	askID := waitForRequest(t, store, "conv", events.TypeInputRequest)
	t.Cleanup(func() {
		d.Broker.Resolve(permID, false, "")
		cancelAsk()
		<-permDone
		<-askDone
	})

	// The fork, taken while both are open in conv.
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/conv/fork", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("fork = %d %s", rec.Code, rec.Body.String())
	}
	var fork struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &fork); err != nil || fork.ID == "" {
		t.Fatalf("fork answered %s: %v", rec.Body.String(), err)
	}

	for _, tc := range []struct {
		session string
		want    []string
	}{
		{"task", []string{"permission.request " + permID}},
		{"conv", []string{"permission.request " + permID, "input.request " + askID}},
		{fork.ID, []string{"permission.request " + permID + " stale", "input.request " + askID + " stale"}},
	} {
		got := questionsIn(openStream(t, srv, tc.session).backlog())
		if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
			t.Errorf("%s was served:\n%s\nwant:\n%s", tc.session, strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
		}
	}
}

// A question that arrives on the live channel is sent as it is, whatever
// the brokers say.
//
// The live channel carries what was written after the stream subscribed,
// and a question written then was recorded by its broker first, so it is
// one this daemon is asking. Marking happens on the backlog alone, after
// it is read. The request here is written straight into the store, which
// no broker knows, so only the path it arrives by decides.
func TestAQuestionAskedAfterTheBacklogIsNotMarked(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	if _, err := d.Loop.Store.CreateSession("s", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(d.Handler())
	t.Cleanup(srv.Close)

	stream := openStream(t, srv, "s")
	if got := questionsIn(stream.backlog()); len(got) != 0 {
		t.Fatalf("an empty log served questions: %v", got)
	}
	if _, err := d.Loop.Store.Append("s", events.TypePermissionRequest, map[string]any{
		"id": "p-live", "tool": "bash", "description": "go test ./...",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Loop.Store.Append("s", events.TypeInputRequest, map[string]any{
		"id": "i-live", "question": "which one?",
	}); err != nil {
		t.Fatal(err)
	}
	// Questions only: a daemon-wide event can arrive on the same stream.
	var got []string
	for len(got) < 2 {
		if ev, ok := stream.next(); ok {
			got = append(got, questionsIn([]events.Event{ev})...)
		}
	}
	want := []string{"permission.request p-live", "input.request i-live"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("live questions sent as:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
