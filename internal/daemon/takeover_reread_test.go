package daemon

import (
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
// model question in each session, and none answered.
func staleStore(t *testing.T, sessions []string) string {
	t.Helper()
	dir := t.TempDir()
	store, err := session.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range sessions {
		if _, err := store.CreateSession(id, "", "general-purpose", true); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []events.Type{events.TypePermissionRequest, events.TypeInputRequest} {
			if _, err := store.Append(id, kind, map[string]any{"id": "old-" + string(kind)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	store.Close()
	return dir
}

// daemonOver builds a daemon over dir the way buildDaemon does. One that
// took over from another then calls NoteTakeover.
func daemonOver(t *testing.T, dir string) (*Daemon, *session.Store) {
	t.Helper()
	store, warnings, err := session.LoadAllFromDisk(dir)
	if err != nil || len(warnings) > 0 {
		t.Fatalf("load: %v %v", err, warnings)
	}
	t.Cleanup(store.Close)
	loop := agent.New(store, tools.NewRegistry(nil), nil, &config.Config{})
	broker := agent.NewPermissionBroker(store)
	loop.Input = agent.NewInputBroker(store)
	loop.RehydrateAll()
	return New(loop, broker, nil, nil, nil, "0.1.0"), store
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
	if _, err := store.Append("S1", events.TypeUserMessage, map[string]any{"text": "next"}); err != nil {
		t.Fatal(err)
	}
	for seq, n := range seqsOnDisk(t, dir, "S1") {
		if n > 1 {
			t.Errorf("S1.jsonl holds seq %d %d times: S1 was not re-read before it was written", seq, n)
		}
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
