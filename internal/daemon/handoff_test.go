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
	"testing"
	"time"

	"localcode/internal/agent"
	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// A daemon over a persisting store, the way a real one is built, with
// nothing served. Enough for the ownership rules, which are about the
// store and the manifest and not about any handler.
func handoffDaemon(t *testing.T) (*Daemon, *session.Store, string) {
	t.Helper()
	dir := t.TempDir()
	store, warnings, err := session.LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if len(warnings) > 0 {
		t.Fatalf("store warnings: %v", warnings)
	}
	// Released before the TempDir is removed: Windows will not delete a
	// file something still has open.
	t.Cleanup(store.Close)
	cfg := &config.Config{}
	loop := agent.New(store, tools.NewRegistry(nil), nil, cfg)
	d := New(loop, agent.NewPermissionBroker(store), nil, nil, nil, "0.1.0")
	return d, store, dir
}

// writeManifest is what the retiring daemon does, done by hand: a list of
// sessions under a pid that is alive. This test process's parent is a pid
// that exists and is not this process, which is what "the other daemon"
// looks like from here.
func writeManifest(t *testing.T, dir string, sessions ...string) {
	t.Helper()
	data, _ := json.Marshal(handoffManifest{PID: os.Getppid(), Sessions: sessions})
	if err := os.WriteFile(filepath.Join(dir, handoffFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSessionIDInPath(t *testing.T) {
	cases := map[string]string{
		"/api/sessions/S1":                  "S1",
		"/api/sessions/S1/messages":         "S1",
		"/api/sessions/S1/schedules/x/seen": "S1",
		"/api/sessions":                     "",
		"/api/sessions/":                    "",
		"/api/version":                      "",
		"/api/workspace":                    "",
	}
	for path, want := range cases {
		if got := sessionIDInPath(path); got != want {
			t.Errorf("sessionIDInPath(%q) = %q, want %q", path, got, want)
		}
	}
}

// A write to a session the previous daemon is still finishing is refused
// with the 409 both clients queue on; a read of it is not, and a write to
// any other session is not.
func TestTheGateRefusesWritesToSessionsStillOwnedElsewhere(t *testing.T) {
	d, store, dir := handoffDaemon(t)
	for _, id := range []string{"S1", "S2"} {
		if _, err := store.CreateSession(id, "", "general-purpose", true); err != nil {
			t.Fatal(err)
		}
	}
	writeManifest(t, dir, "S1")
	d.NoteTakeover()

	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	post := func(path string) int {
		resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(`{"title":"x"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	get := func(path string) int {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := post("/api/sessions/S1/rename"); code != http.StatusConflict {
		t.Errorf("a write to an owned session answered %d, want 409", code)
	}
	if code := get("/api/sessions/S1"); code == http.StatusConflict {
		t.Error("a read of an owned session was refused; reads must pass")
	}
	if code := post("/api/sessions/S2/rename"); code == http.StatusConflict {
		t.Error("a write to a session nobody else owns was refused")
	}
}

// A manifest left by a daemon that died mid-drain must not lock its
// sessions forever. A pid nothing answers for is the sign, and the file
// goes.
func TestAManifestFromADeadDaemonIsDiscarded(t *testing.T) {
	d, store, dir := handoffDaemon(t)
	if _, err := store.CreateSession("S1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	// A pid that is certainly not running: the largest a system hands out
	// is far below this, and FindProcess+Signal(0) on it fails.
	data, _ := json.Marshal(handoffManifest{PID: 1 << 30, Sessions: []string{"S1"}})
	path := filepath.Join(dir, handoffFile)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if d.ownedElsewhere("S1") {
		t.Error("a session was reported owned by a daemon that does not exist")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the stale manifest was left in place")
	}
}

// The copy a daemon loaded at startup predates the old daemon's last
// writes to a session it still owned. Taking the session over has to read
// it again, or the first append would reuse a sequence number already on
// disk and the replay would be missing what the old daemon wrote.
func TestTakingOverASessionRereadsWhatTheOldDaemonWrote(t *testing.T) {
	d, store, dir := handoffDaemon(t)
	if _, err := store.CreateSession("S1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("S1", events.TypeUserMessage, map[string]any{"text": "first"}); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, dir, "S1")
	d.NoteTakeover()

	// The old daemon finishes its turn: two more events land on disk
	// behind this store's back, the way another process's writes do.
	f, err := os.OpenFile(filepath.Join(dir, "S1.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{"second", "third"} {
		line, _ := json.Marshal(events.Event{Seq: uint64(2 + i), Type: events.TypeMessagePartEnd, Data: map[string]any{"text": text}})
		fmt.Fprintf(f, "%s\n", line)
	}
	f.Close()
	// And releases the session.
	os.Remove(filepath.Join(dir, handoffFile))

	if err := d.claimSession("S1"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	evs, _ := store.Events("S1", 0)
	if len(evs) != 3 {
		t.Fatalf("after taking over, the store holds %d events, want 3 (the old daemon's writes were not re-read)", len(evs))
	}
	ev, err := store.Append("S1", events.TypeUserMessage, map[string]any{"text": "fourth"})
	if err != nil {
		t.Fatal(err)
	}
	if ev.Seq != 4 {
		t.Errorf("the first append after taking over got seq %d, want 4; a seq already on disk was reused", ev.Seq)
	}
}

// Retiring: what is in flight is waited for, streams are told, and the
// manifest tracks the sessions still owned until nothing is.
func TestRetireWaitsForTheTurnAndTellsTheStreams(t *testing.T) {
	d, store, dir := handoffDaemon(t)
	if _, err := store.CreateSession("S1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	// A turn in flight on S1, as the tracker and the lifecycle would see
	// it: the tracker names the session, the lifecycle counts the work.
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !d.turns.begin("S1", cancel) {
		t.Fatal("begin")
	}
	release, ok := d.Loop.AdmitTopLevel()
	if !ok {
		t.Fatal("admit")
	}

	live, lost, unsub, err := store.Subscribe("S1")
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	daemonLive, unsubDaemon := d.daemonEvents.subscribe()
	defer unsubDaemon()

	done := make(chan bool, 1)
	go func() { done <- d.Retire(context.Background(), "9.9.9", 4242) }()

	// While the turn runs: the manifest names S1, and the stream has been
	// told who is taking over.
	select {
	case ev := <-daemonLive:
		if ev.Type != events.TypeDaemonReplaced {
			t.Errorf("stream got %s first, want daemon.replaced", ev.Type)
		}
		if v, _ := ev.Data["version"].(string); v != "9.9.9" {
			t.Errorf("daemon.replaced names version %q, want 9.9.9", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no daemon.replaced on the stream")
	}
	data, err := os.ReadFile(filepath.Join(dir, handoffFile))
	if err != nil {
		t.Fatalf("no manifest while a turn is in flight: %v", err)
	}
	if !strings.Contains(string(data), `"S1"`) {
		t.Errorf("manifest does not name the owned session: %s", data)
	}
	select {
	case <-done:
		t.Fatal("Retire returned while a turn was still in flight")
	case <-time.After(300 * time.Millisecond):
	}

	// The turn ends.
	d.turns.end("S1")
	release()

	select {
	case finished := <-done:
		if !finished {
			t.Error("Retire reported not finishing although the turn ended")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Retire did not return after the turn ended")
	}
	if _, err := os.Stat(filepath.Join(dir, handoffFile)); !os.IsNotExist(err) {
		t.Error("the manifest was left behind after everything finished")
	}
	select {
	case <-lost:
	case <-time.After(time.Second):
		t.Error("the session stream was not ended")
	}
	_ = live
}

// Retire gives up waiting when told to, and says so.
func TestRetireGivesUpOnADeadline(t *testing.T) {
	d, store, _ := handoffDaemon(t)
	if _, err := store.CreateSession("S1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	release, ok := d.Loop.AdmitTopLevel()
	if !ok {
		t.Fatal("admit")
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if d.Retire(ctx, "9.9.9", 1) {
		t.Error("Retire reported finishing although the work never ended")
	}
}

// A retiring daemon lets go of the session logs.
//
// It used to hold every one of them open for the rest of its process's
// life, which after a handoff is the life of the window or the terminal —
// while the successor is the daemon actually being asked to write and
// delete them. On Windows that made "delete session" impossible: the file
// cannot be removed while another process has it open, and the retired
// daemon was that other process.
func TestARetiredDaemonLetsGoOfTheSessionLogs(t *testing.T) {
	d, store, dir := handoffDaemon(t)
	if _, err := store.CreateSession("S1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	store.Append("S1", events.TypeUserMessage, map[string]any{"text": "before"})

	log := filepath.Join(dir, "S1.jsonl")
	before, err := os.Stat(log)
	if err != nil {
		t.Fatalf("the session log was never written: %v", err)
	}

	if !d.Retire(context.Background(), "9.9.9", 4242) {
		t.Fatal("Retire did not finish")
	}

	// The handle is gone, so this reaches nothing. That is the observable
	// half of "released" that does not depend on the platform's rules
	// about deleting open files.
	store.Append("S1", events.TypeUserMessage, map[string]any{"text": "after retiring"})
	after, err := os.Stat(log)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() {
		t.Errorf("the retired daemon is still writing to %s: %d bytes, was %d",
			log, after.Size(), before.Size())
	}
	// And what the bug actually broke. The whole directory, not just the
	// log: on Windows an open handle on any file in it — the log, the
	// .meta.json, a checkpoint — refuses the removal, so this is the one
	// assertion that covers every file a retiring daemon might still be
	// holding rather than the one that was found holding it.
	if err := os.RemoveAll(dir); err != nil {
		t.Errorf("the retired daemon is still holding something in %s: %v", dir, err)
	}
}

// Retire waits for the turn itself, not only for the claims around it.
//
// Loop.Drain waits on admission windows and background tasks, and a
// turn's admission window closes the moment the turn is registered:
// handleSendMessage releases it, answers 202 and leaves the turn to a
// goroutine. So Retire returned in under a millisecond with a reply still
// being written — which left the session named in .handoff.json for the
// life of the retiring process (the successor then refused it with 409
// forever) and, once the store was closed under it, dropped the model's
// answer without a word.
func TestRetireWaitsForATurnThatOutlivesItsAdmissionWindow(t *testing.T) {
	d, store, dir := handoffDaemon(t)
	if _, err := store.CreateSession("S1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}

	// Exactly what handleSendMessage does: take the admission window,
	// register the turn, then give the window back and let the turn run.
	release, ok := d.Loop.AdmitTopLevel()
	if !ok {
		t.Fatal("admit")
	}
	if !d.turns.begin("S1", func() {}) {
		t.Fatal("begin")
	}
	release()

	retired := make(chan bool, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		retired <- d.Retire(ctx, "9.9.9", 4242)
	}()

	select {
	case <-retired:
		t.Fatal("Retire finished while a turn was still running")
	case <-time.After(300 * time.Millisecond):
	}

	// The turn writes its answer while Retire is waiting. It has to reach
	// the file: the retiring process is the only one that has it.
	store.Append("S1", events.TypeMessagePartEnd, map[string]any{"text": "the answer"})
	d.turns.end("S1")

	select {
	case ok := <-retired:
		if !ok {
			t.Error("Retire reported it had not finished, although the turn ended in time")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Retire never returned after the turn ended")
	}

	log, err := os.ReadFile(filepath.Join(dir, "S1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "the answer") {
		t.Errorf("the reply written during the handoff is not in the log:\n%s", log)
	}

	// And nothing this daemon owns is left in the manifest for the
	// successor to refuse.
	if data, err := os.ReadFile(filepath.Join(dir, handoffFile)); err == nil {
		var m handoffManifest
		_ = json.Unmarshal(data, &m)
		if len(m.Sessions) != 0 {
			t.Errorf("the retired daemon still claims %v; the successor will refuse them with 409", m.Sessions)
		}
	}
}
