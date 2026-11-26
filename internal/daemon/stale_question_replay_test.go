package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	return openStreamAs(t, srv, id, "", "")
}

// openStreamAs opens it the way a client does: with the query it sends
// and, for a browser's own reconnect, a Last-Event-ID.
func openStreamAs(t *testing.T, srv *httptest.Server, id, query, lastEventID string) *sseReader {
	t.Helper()
	prev := heartbeatInterval
	heartbeatInterval = 30 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = prev })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/sessions/"+id+"/events"+query, nil)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream of %s%s: %v", id, query, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open stream of %s%s: %s", id, query, resp.Status)
	}
	return &sseReader{t: t, scanner: bufio.NewScanner(resp.Body)}
}

// drain reads r until the daemon ends the stream.
func (r *sseReader) drain() []events.Event {
	r.t.Helper()
	var out []events.Event
	for r.scanner.Scan() {
		payload, ok := strings.CutPrefix(r.scanner.Text(), "data: ")
		if !ok {
			continue
		}
		var ev events.Event
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			r.t.Fatalf("bad frame %q: %v", payload, err)
		}
		out = append(out, ev)
	}
	return out
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

// leftOpen is a daemon over a log whose daemon stopped while asking both
// kinds of question, and the log's own copy of its events.
func leftOpen(t *testing.T, id string) (*httptest.Server, []events.Event) {
	t.Helper()
	dir := t.TempDir()
	writeSessionLog(t, dir, id, []events.Event{
		logEvent(1, events.TypeUserMessage, "clean up"),
		question(2, events.TypePermissionRequest, "d1-p1"),
		question(3, events.TypeInputRequest, "d1-i1"),
	})
	store, _, err := session.LoadAllFromDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	d := newTestDaemonOver(t, store, "http://127.0.0.1:1")
	srv := httptest.NewServer(d.Handler())
	t.Cleanup(srv.Close)
	logged, err := store.Events(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	return srv, logged
}

// Every stream a client opens is marked, not only one with no query,
// which is what the Web UI's "show earlier" sends. The TUI sends ?since=
// (0 on a session switch), the Web UI opens with ?tail= and resumes with
// ?since=, and a browser reconnecting on its own sends Last-Event-ID.
func TestEveryStreamAClientOpensIsMarked(t *testing.T) {
	const id = "s-shapes"
	srv, _ := leftOpen(t, id)
	want := "permission.request d1-p1 stale\ninput.request d1-i1 stale"
	for _, tc := range []struct{ name, query, lastEventID string }{
		{"TUI session switch", "?since=0", ""},
		{"TUI reattach", "?since=1", ""},
		{"Web UI open", "?tail=400", ""},
		{"Web UI resume", "?since=1", ""},
		{"browser reconnect", "?tail=400", "1"},
	} {
		got := strings.Join(questionsIn(openStreamAs(t, srv, id, tc.query, tc.lastEventID).backlog()), "\n")
		if got != want {
			t.Errorf("%s (%s, Last-Event-ID %q) was served:\n%s\nwant:\n%s", tc.name, tc.query, tc.lastEventID, got, want)
		}
	}
}

// A stale copy is the request with "stale": true added and nothing else
// changed: the Web UI draws the model's question and its options from
// it, and the task window "[tool] description".
func TestAStaleCopyIsTheRequestAsLogged(t *testing.T) {
	const id = "s-fields"
	srv, logged := leftOpen(t, id)
	for i, ev := range openStream(t, srv, id).backlog() {
		if ev.Type != events.TypePermissionRequest && ev.Type != events.TypeInputRequest {
			continue
		}
		if ev.Seq != logged[i].Seq {
			t.Errorf("%s %v served with seq %d, logged with %d", ev.Type, ev.Data["id"], ev.Seq, logged[i].Seq)
		}
		for k, v := range logged[i].Data {
			if got, ok := ev.Data[k]; !ok || got != v {
				t.Errorf("%s %v: %q served as %v, logged as %v", ev.Type, ev.Data["id"], k, got, v)
			}
		}
		if len(ev.Data) != len(logged[i].Data)+1 {
			t.Errorf("%s %v served with %d keys, want the logged %d plus stale", ev.Type, ev.Data["id"], len(ev.Data), len(logged[i].Data))
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

// A question the retiring daemon of an in-place update asked during the
// drain and left open reaches the successor's clients on the reconnect
// the takeover forces: Reload ends every stream of the session, and the
// browser comes back to the same ?tail= URL with Last-Event-ID. That
// backlog carries the question stale. The successor's own question,
// asked once it owns the session, is served as asked.
func TestATakeoverServesTheRetiringDaemonsQuestionStale(t *testing.T) {
	dir := t.TempDir()
	old, err := session.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.CreateSession("S1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	old.Append("S1", events.TypeUserMessage, map[string]any{"text": "clean up"})
	old.Close()
	writeManifest(t, dir, "S1")
	d, store := daemonOver(t, dir)
	srv := httptest.NewServer(d.Handler())
	t.Cleanup(srv.Close)

	// A browser opens S1 on the successor during the drain.
	stream := openStreamAs(t, srv, "S1", "?tail=400", "")
	if got := questionsIn(stream.backlog()); len(got) != 0 {
		t.Fatalf("questions before the retiring daemon asked any: %v", got)
	}
	// The retiring daemon asks, stops without an answer, and lets go.
	appendOnDisk(t, dir, "S1", events.Event{Seq: 2, Type: events.TypePermissionRequest, Timestamp: time.Unix(0, 0).UTC(),
		Data: map[string]any{"id": "old-p1", "tool": "bash", "description": "make test"}})
	if err := os.Remove(filepath.Join(dir, handoffFile)); err != nil {
		t.Fatal(err)
	}
	if err := d.claimSession("S1"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	stream.drain() // Reload ends the stream

	got := strings.Join(questionsIn(openStreamAs(t, srv, "S1", "?tail=400", "1").backlog()), "\n")
	if want := "permission.request old-p1 stale"; got != want {
		t.Errorf("the reconnect after the takeover was served:\n%s\nwant:\n%s", got, want)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.Broker.Func()(agent.WithSessionID(context.Background(), "S1"),
			tools.Ask{Tool: "bash", Subject: "go test ./...", Description: "go test ./..."})
	}()
	var mine string
	deadline := time.Now().Add(5 * time.Second)
	for mine == "" {
		evs, _ := store.Events("S1", 2)
		for _, ev := range evs {
			if ev.Type == events.TypePermissionRequest {
				mine, _ = ev.Data["id"].(string)
			}
		}
		if mine == "" && time.Now().After(deadline) {
			t.Fatal("the successor never asked")
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Cleanup(func() { d.Broker.Resolve(mine, false, ""); <-done })
	got = strings.Join(questionsIn(openStreamAs(t, srv, "S1", "?tail=400", "1").backlog()), "\n")
	if want := "permission.request old-p1 stale\npermission.request " + mine; got != want {
		t.Errorf("after the successor asked, the reconnect was served:\n%s\nwant:\n%s", got, want)
	}
}

// resolutionsIn returns, for each transient resolution in evs (no seq,
// marked stale), its type, id and the fields a client reads from it.
func resolutionsIn(evs []events.Event) []string {
	var out []string
	for _, ev := range evs {
		if ev.Type != events.TypePermissionResolved && ev.Type != events.TypeInputResolved {
			continue
		}
		if stale, _ := ev.Data["stale"].(bool); !stale {
			continue
		}
		id, _ := ev.Data["id"].(string)
		line := fmt.Sprintf("%s %s seq=%d", ev.Type, id, ev.Seq)
		if ev.Type == events.TypePermissionResolved {
			line += fmt.Sprintf(" allow=%v unanswered=%v", ev.Data["allow"], ev.Data["unanswered"])
		} else {
			line += fmt.Sprintf(" cancelled=%v", ev.Data["cancelled"])
		}
		out = append(out, line)
	}
	return out
}

// A client that resumes a stream is told which questions before its
// resume point nobody is waiting on, so it can put away one it still
// holds: it saw the question live, the daemon that asked stopped, and the
// backlog after the resume point carries no copy of it to mark.
//
// Each is a resolution with no seq, sent to this stream alone and not
// written anywhere. Not for a question answered anywhere in the log, one
// with no id, one this daemon is waiting on, or one after the resume
// point, which the backlog carries marked. And not to a stream that is
// not a resume: a fresh client holds nothing.
func TestAResumingStreamIsToldWhichQuestionsNobodyIsWaitingOn(t *testing.T) {
	dir := t.TempDir()
	const id = "s-resume-stale"
	writeSessionLog(t, dir, id, []events.Event{
		logEvent(1, events.TypeUserMessage, "go"),
		question(2, events.TypePermissionRequest, "d1-p1"),
		question(3, events.TypeInputRequest, "d1-i1"),
		question(4, events.TypePermissionRequest, "d1-p2"),
		answer(5, events.TypePermissionResolved, "d1-p2"),
		question(6, events.TypePermissionRequest, "d1-p3"),
		question(7, events.TypePermissionRequest, ""),
		logEvent(8, events.TypeUserMessage, "next"),
		answer(9, events.TypePermissionResolved, "d1-p3"),
		question(10, events.TypePermissionRequest, "d1-p4"),
	})
	store, _, err := session.LoadAllFromDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	d := newTestDaemonOver(t, store, "http://127.0.0.1:1")
	srv := httptest.NewServer(d.Handler())
	t.Cleanup(srv.Close)

	// A question this daemon is asking, at seq 11.
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.Broker.Func()(agent.WithSessionID(context.Background(), id),
			tools.Ask{Tool: "bash", Subject: "go test ./...", Description: "go test ./..."})
	}()
	var live string
	deadline := time.Now().Add(5 * time.Second)
	for live == "" {
		evs, _ := store.Events(id, 10)
		for _, ev := range evs {
			if ev.Type == events.TypePermissionRequest {
				live, _ = ev.Data["id"].(string)
			}
		}
		if live == "" && time.Now().After(deadline) {
			t.Fatal("this daemon never asked")
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Cleanup(func() { d.Broker.Resolve(live, false, ""); <-done })

	join := func(s []string) string { return strings.Join(s, "\n") }
	for _, tc := range []struct {
		name, query, lastEventID string
		resolutions, questions   []string
	}{
		{
			name: "resume after seq 8", query: "?since=8",
			resolutions: []string{
				"permission.resolved d1-p1 seq=0 allow=false unanswered=true",
				"input.resolved d1-i1 seq=0 cancelled=true",
			},
			questions: []string{"permission.request d1-p4 stale", "permission.request " + live},
		},
		{
			name: "browser reconnect after seq 11", query: "?tail=400", lastEventID: "11",
			resolutions: []string{
				"permission.resolved d1-p1 seq=0 allow=false unanswered=true",
				"input.resolved d1-i1 seq=0 cancelled=true",
				"permission.resolved d1-p4 seq=0 allow=false unanswered=true",
			},
		},
		{name: "fresh open", query: ""},
		{
			// A cut after d1-p1 and d1-i1, so the stream starts past
			// questions it is not told about.
			name: "fresh open at the end", query: "?tail=2",
			questions: []string{"permission.request d1-p4 stale", "permission.request " + live},
		},
		{name: "fresh open from the start", query: "?since=0"},
	} {
		served := openStreamAs(t, srv, id, tc.query, tc.lastEventID).backlog()
		if got := resolutionsIn(served); join(got) != join(tc.resolutions) {
			t.Errorf("%s (%s, Last-Event-ID %q) was told:\n%s\nwant:\n%s", tc.name, tc.query, tc.lastEventID, join(got), join(tc.resolutions))
		}
		if tc.questions != nil {
			if got := questionsIn(served); join(got) != join(tc.questions) {
				t.Errorf("%s was served questions:\n%s\nwant:\n%s", tc.name, join(got), join(tc.questions))
			}
		}
	}

	// Nothing was written.
	evs, err := store.Events(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if stale, _ := ev.Data["stale"].(bool); stale {
			t.Errorf("seq %d %s in the log carries stale: a resolution was written", ev.Seq, ev.Type)
		}
	}
}
