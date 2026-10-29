package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"localcode/internal/agent"
	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// staleStore writes, through a store that is then closed, the log a
// daemon leaves when it stops while asking: a permission question and a
// model question in each session, and none answered. "parent/task" names
// a background task under parent. archived names the sessions to put on
// the shelf.
func staleStore(t *testing.T, sessions []string, archived ...string) string {
	t.Helper()
	dir := t.TempDir()
	store, err := session.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range sessions {
		parent := ""
		if p, task, ok := strings.Cut(id, "/"); ok {
			parent, id = p, task
		}
		if _, err := store.CreateSession(id, parent, "general-purpose", parent == ""); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []events.Type{events.TypePermissionRequest, events.TypeInputRequest} {
			if _, err := store.Append(id, kind, map[string]any{"id": "old-" + string(kind)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, id := range archived {
		if _, err := store.Archive(id); err != nil {
			t.Fatal(err)
		}
	}
	store.Close()
	return dir
}

// daemonOver builds a daemon over dir the way buildDaemon does. A daemon
// that starts on its own then calls CloseStaleQuestionsAtStart; one that
// took over from another calls NoteTakeover.
func daemonOver(t *testing.T, dir string) (*Daemon, *session.Store) {
	t.Helper()
	store, warnings, err := session.LoadAllFromDisk(dir)
	if err != nil || len(warnings) > 0 {
		t.Fatalf("load: %v %v", err, warnings)
	}
	t.Cleanup(store.Close)
	loop := agent.New(store, tools.NewRegistry(nil), nil, &config.Config{})
	broker := agent.NewPermissionBroker(store)
	loop.PermissionShownIn = broker.ShownIn
	loop.Input = agent.NewInputBroker(store)
	loop.RehydrateAll()
	return New(loop, broker, nil, nil, nil, "0.1.0"), store
}

// closed reports which question ids a session's log resolves.
func closed(t *testing.T, store *session.Store, id string) map[string]bool {
	t.Helper()
	evs, err := store.Events(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, ev := range evs {
		if ev.Type == events.TypePermissionResolved || ev.Type == events.TypeInputResolved {
			id, _ := ev.Data["id"].(string)
			out[id] = true
		}
	}
	return out
}

// seqsOnDisk counts each sequence number in a session's file. Two events
// under one number break since= replay for that session for good.
func seqsOnDisk(t *testing.T, dir, id string) map[uint64]int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, id+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[uint64]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var ev events.Event
		if json.Unmarshal([]byte(line), &ev) == nil {
			out[ev.Seq]++
		}
	}
	return out
}

func TestAStartingDaemonClosesTheQuestionsAStoppedOneLeftOpen(t *testing.T) {
	dir := staleStore(t, []string{"open", "shelf", "shelf/task"}, "shelf")
	d, store := daemonOver(t, dir)
	d.CloseStaleQuestionsAtStart()

	if got := closed(t, store, "open"); len(got) != 2 {
		t.Errorf("open: closed %v, want both of the stopped daemon's questions", got)
	}
	// Retrieving a shelved conversation closes its questions; startup
	// does not read its log at all.
	if got := closed(t, store, "shelf"); len(got) != 0 {
		t.Errorf("shelf: closed %v at startup, want nothing until it is retrieved", got)
	}
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/shelf/retrieve", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("retrieve = %d %s", rec.Code, rec.Body.String())
	}
	// The task's own log too: it was on the shelf with its conversation.
	for _, id := range []string{"shelf", "task"} {
		if got := closed(t, store, id); len(got) != 2 {
			t.Errorf("%s: closed %v after retrieval, want both questions", id, got)
		}
	}
}

// A successor starts while the daemon it replaces is still finishing a
// turn in S1 and may still be waiting on S1's question. It writes to no
// log on its way up: the manifest does not name everything the retiring
// daemon may still write (a scheduled run is not listed), so S2 is not
// safe to close either. Each is closed when the successor first writes
// it, S1 only after S1 is released and re-read.
func TestASuccessorLeavesTheRetiringDaemonsQuestionsAlone(t *testing.T) {
	dir := staleStore(t, []string{"S1", "S2"})
	writeManifest(t, dir, "S1")
	d, store := daemonOver(t, dir)
	d.NoteTakeover()

	for _, id := range []string{"S1", "S2"} {
		if got := closed(t, store, id); len(got) != 0 {
			t.Errorf("%s: the successor closed %v on its way up", id, got)
		}
	}
	if err := d.claimSession("S2"); err != nil {
		t.Fatalf("claim S2: %v", err)
	}
	if got := closed(t, store, "S2"); len(got) != 2 {
		t.Errorf("S2: closed %v when first written, want both questions", got)
	}

	// The retiring daemon answers its own question, in its own copy of
	// the log, and is then stopped before it answers the other one.
	f, err := os.OpenFile(filepath.Join(dir, "S1.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := json.Marshal(events.Event{Seq: 3, Type: events.TypePermissionResolved,
		Data: map[string]any{"id": "old-" + string(events.TypePermissionRequest), "allow": true, "scope": "once"}})
	f.Write(append(line, '\n'))
	f.Close()
	if err := os.Remove(filepath.Join(dir, handoffFile)); err != nil {
		t.Fatal(err)
	}

	// Taking it over re-reads it, and only then closes what is left.
	if err := d.claimSession("S1"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	got := closed(t, store, "S1")
	if len(got) != 2 {
		t.Errorf("S1 after takeover: closed %v, want the answered question and the one left open", got)
	}
	for seq, n := range seqsOnDisk(t, dir, "S1") {
		if n > 1 {
			t.Errorf("S1.jsonl holds seq %d %d times", seq, n)
		}
	}
	evs, _ := store.Events("S1", 0)
	allowed := 0
	for _, ev := range evs {
		if ev.Type == events.TypePermissionResolved {
			allowed++
		}
	}
	if allowed != 1 {
		t.Errorf("S1 has %d permission resolutions, want the retiring daemon's one: %v", allowed, evs)
	}
}

// The retiring daemon lets go of S1 between construction, which read the
// manifest, and NoteTakeover, which reads it again. S1 was still being
// written after this daemon loaded it, so it is still re-read and its
// questions closed only then.
func TestASessionReleasedBeforeTheSuccessorLooksIsStillTakenOver(t *testing.T) {
	dir := staleStore(t, []string{"S1"})
	writeManifest(t, dir, "S1")
	d, store := daemonOver(t, dir)

	// The retiring daemon answers one question, after this daemon loaded
	// S1, and lets go before NoteTakeover reads the manifest.
	f, err := os.OpenFile(filepath.Join(dir, "S1.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := json.Marshal(events.Event{Seq: 3, Type: events.TypeInputResolved,
		Data: map[string]any{"id": "old-" + string(events.TypeInputRequest), "answer": "SQLite"}})
	f.Write(append(line, '\n'))
	f.Close()
	if err := os.Remove(filepath.Join(dir, handoffFile)); err != nil {
		t.Fatal(err)
	}
	d.NoteTakeover()
	if err := d.claimSession("S1"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if got := closed(t, store, "S1"); len(got) != 2 {
		t.Errorf("S1: closed %v after takeover, want both questions", got)
	}
	for seq, n := range seqsOnDisk(t, dir, "S1") {
		if n > 1 {
			t.Errorf("S1.jsonl holds seq %d %d times: S1 was not re-read before it was written", seq, n)
		}
	}
}

// A fork copies a background task's question mirrored into its parent.
// Nothing in the fork can answer it, so the fork closes it; the parent
// keeps showing it, and the task still gets its answer.
func TestAForkClosesTheQuestionsItCopied(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	if _, err := d.Loop.Store.CreateSession("p", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Loop.Store.CreateSession("task", "p", "general-purpose", false); err != nil {
		t.Fatal(err)
	}
	allowed := make(chan bool, 1)
	go func() {
		ok, _ := d.Broker.Func()(agent.WithSessionID(context.Background(), "task"),
			tools.Ask{Tool: "bash", Subject: "make test", Description: "make test"})
		allowed <- ok
	}()
	asked := ""
	deadline := time.Now().Add(5 * time.Second)
	for asked == "" {
		evs, _ := d.Loop.Store.Events("p", 0)
		for _, ev := range evs {
			if ev.Type == events.TypePermissionRequest {
				asked, _ = ev.Data["id"].(string)
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the task's question was never mirrored into p")
		}
		time.Sleep(5 * time.Millisecond)
	}

	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/p/fork", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("fork = %d %s", rec.Code, rec.Body.String())
	}
	var fork session.Session
	if err := json.Unmarshal(rec.Body.Bytes(), &fork); err != nil {
		t.Fatal(err)
	}
	if !closed(t, d.Loop.Store, fork.ID)[asked] {
		t.Errorf("the fork %s shows %s open; nothing in it can answer it", fork.ID, asked)
	}
	if closed(t, d.Loop.Store, "p")[asked] {
		t.Errorf("forking closed %s in p, where the task is still waiting on it", asked)
	}
	if !d.Broker.Resolve(asked, true, agent.ScopeOnce) || !<-allowed {
		t.Error("the task's question could not be answered after the fork")
	}
}

// The retiring daemon may own nothing it lists and still be writing: it
// admits work until its drain begins. A successor writes nothing on its
// way up, manifest or not.
func TestASuccessorWithNoManifestWritesNothingOnItsWayUp(t *testing.T) {
	dir := staleStore(t, []string{"B"})
	d, store := daemonOver(t, dir)
	d.NoteTakeover()
	if evs, _ := store.Events("B", 0); len(evs) != 2 {
		t.Errorf("B: the successor wrote on its way up: %v", evs)
	}
}

// A session taken over is re-read, and the model's history is rebuilt
// from what was read. It was cleared and never rebuilt, so the first
// turn after an update sent the model none of the conversation.
func TestATakenOverSessionKeepsItsHistory(t *testing.T) {
	var mu sync.Mutex
	var lastMessages []map[string]any
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		lastMessages = body.Messages
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer model.Close()

	dir := t.TempDir()
	old, err := session.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.CreateSession("S1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	old.Append("S1", events.TypeUserMessage, map[string]any{"text": "remember the number 41"})
	old.Append("S1", events.TypeMessagePartEnd, map[string]any{"text": "noted: 41"})
	old.Close()
	writeManifest(t, dir, "S1")

	store, warnings, err := session.LoadAllFromDisk(dir)
	if err != nil || len(warnings) > 0 {
		t.Fatalf("load: %v %v", err, warnings)
	}
	t.Cleanup(store.Close)
	d := newTestDaemonOver(t, store, model.URL)
	d.Loop.RehydrateAll()
	d.NoteTakeover()
	if err := os.Remove(filepath.Join(dir, handoffFile)); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/S1/messages", strings.NewReader(`{"text":"what number?"}`)))
	if rec.Code/100 != 2 {
		t.Fatalf("send = %d %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		msgs := lastMessages
		mu.Unlock()
		if len(msgs) > 0 {
			raw, _ := json.Marshal(msgs)
			if !strings.Contains(string(raw), "remember the number 41") {
				t.Errorf("the first turn after taking S1 over sent the model %s, without the conversation before it", raw)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the model was never asked")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A daemon that starts on its own while a manifest names a live daemon
// still writing S1 leaves S1 to that daemon.
func TestTheStartupCloseSkipsWhatAnotherDaemonIsWriting(t *testing.T) {
	dir := staleStore(t, []string{"S1", "S2"})
	writeManifest(t, dir, "S1")
	d, store := daemonOver(t, dir)
	d.CloseStaleQuestionsAtStart()
	if got := closed(t, store, "S1"); len(got) != 0 {
		t.Errorf("S1: closed %v while another daemon is listed as writing it", got)
	}
	if got := closed(t, store, "S2"); len(got) != 2 {
		t.Errorf("S2: closed %v, want both questions", got)
	}
}
