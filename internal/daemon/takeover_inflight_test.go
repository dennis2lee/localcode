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

// appendOnDisk is the retiring daemon writing a session after this one
// loaded it: the events land in the file behind this store's back.
func appendOnDisk(t *testing.T, dir, id string, evs ...events.Event) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, id+".jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, ev := range evs {
		line, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}

// askedModel is a model that records the text of every request it gets
// and answers each one "ok".
func askedModel(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var asked []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw, _ := json.Marshal(body.Messages)
		mu.Lock()
		asked = append(asked, string(raw))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	t.Cleanup(model.Close)
	return model, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), asked...)
	}
}

// waitForAsk returns the first request to the model that contains want.
func waitForAsk(t *testing.T, asked func() []string, want string) string {
	t.Helper()
	deadline := time.Now().Add(mustHappen)
	for time.Now().Before(deadline) {
		for _, req := range asked() {
			if strings.Contains(req, want) {
				return req
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the model was never asked about %q; it was asked %v", want, asked())
	return ""
}

// successorOver is a successor with a model over a sessions directory the
// retiring daemon listed S1 in. Construction reads the manifest, which is
// what makes S1 suspect; NoteTakeover is left out so no watcher races the
// test for the takeover.
func successorOver(t *testing.T, dir, modelURL string) (*Daemon, *session.Store) {
	t.Helper()
	store, warnings, err := session.LoadAllFromDisk(dir)
	if err != nil || len(warnings) > 0 {
		t.Fatalf("load: %v %v", err, warnings)
	}
	t.Cleanup(store.Close)
	d := newTestDaemonOver(t, store, modelURL)
	d.Loop.RehydrateAll()
	return d, store
}

// A write that arrives while a takeover is re-reading the session waits
// for the re-read to finish. It used to be let through the moment the
// takeover began, because the session was marked taken over before it
// was read, and the turn it started sent the model the cleared history:
// the new message and nothing before it.
func TestAWriteDuringATakeoverWaitsForIt(t *testing.T) {
	model, asked := askedModel(t)
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
	d, _ := successorOver(t, dir, model.URL)

	// The retiring daemon's last turn in S1, after this daemon loaded it,
	// and then it lets go.
	appendOnDisk(t, dir, "S1",
		events.Event{Seq: 3, Type: events.TypeUserMessage, Data: map[string]any{"text": "and the number 42"}},
		events.Event{Seq: 4, Type: events.TypeMessagePartEnd, Data: map[string]any{"text": "noted: 42"}})
	if err := os.Remove(filepath.Join(dir, handoffFile)); err != nil {
		t.Fatal(err)
	}

	arrived, release := holdAtHook(t, &takeoverBarrier)
	defer release()
	took := make(chan error, 1)
	// As watchTakeover calls it, the moment it sees the release.
	go func() { took <- d.takeOwnership("S1") }()
	select {
	case <-arrived:
	case <-time.After(mustHappen):
		t.Fatal("the takeover never reached its re-read")
	}

	sent := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/S1/messages",
			strings.NewReader(`{"text":"which numbers?"}`)))
		sent <- rec
	}()
	select {
	case rec := <-sent:
		t.Errorf("a write answered %d while S1 was being re-read; it has to wait for the re-read", rec.Code)
		sent <- rec
	// Waiting to be sure something does not happen: see the comment in
	// TestRetireWaitsForTheTurnAndTellsTheStreams.
	case <-time.After(300 * time.Millisecond):
	}
	release()
	if err := <-took; err != nil {
		t.Fatalf("takeover: %v", err)
	}
	select {
	case rec := <-sent:
		if rec.Code/100 != 2 {
			t.Fatalf("send = %d %s", rec.Code, rec.Body.String())
		}
	case <-time.After(mustHappen):
		t.Fatal("the write never went through after the takeover finished")
	}

	req := waitForAsk(t, asked, "which numbers?")
	for _, want := range []string{"remember the number 41", "and the number 42"} {
		if !strings.Contains(req, want) {
			t.Errorf("the first turn after taking S1 over sent the model %s, without %q", req, want)
		}
	}
}

// A session is in the store for the whole of a re-read. Reload deleted it
// and added it back, so a write that reached it in between was answered
// 404, and a client opening it or listing sessions was told it did not
// exist.
func TestASessionIsNeverMissingWhileItIsReread(t *testing.T) {
	dir := staleStore(t, []string{"S1"})
	_, store := daemonOver(t, dir)

	stop := make(chan struct{})
	missing := make(chan int, 1)
	go func() {
		n := 0
		for {
			select {
			case <-stop:
				missing <- n
				return
			default:
			}
			if _, err := store.Get("S1"); err != nil {
				n++
			}
		}
	}()
	for range 300 {
		if err := store.Reload("S1"); err != nil {
			close(stop)
			t.Fatalf("reload: %v", err)
		}
	}
	close(stop)
	if n := <-missing; n > 0 {
		t.Errorf("S1 was missing from the store %d times while it was re-read", n)
	}
	if evs, err := store.Events("S1", 0); err != nil || len(evs) != 2 {
		t.Errorf("after the re-reads S1 holds %d events (%v), want the 2 on disk", len(evs), err)
	}
}

// A booking the retiring daemon made in a session it was still writing is
// in the books once the session is taken over, and fires; one it
// cancelled there is gone. The books were read once, at startup, from
// the copy the takeover exists to replace, so the new booking was never
// listed and never fired, and the cancelled one stayed armed.
func TestATakenOverSessionsBookingsAreReread(t *testing.T) {
	model, asked := askedModel(t)
	dir := t.TempDir()
	old, err := session.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.CreateSession("S1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	old.Append("S1", events.TypeScheduleCreated, map[string]any{
		"id": "s1", "at": time.Now().Add(time.Hour).Format(time.RFC3339),
		"prompt": "the cancelled prompt", "agent": "general-purpose",
	})
	old.Close()
	writeManifest(t, dir, "S1")
	d, _ := successorOver(t, dir, model.URL)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// As wire.go builds and restores it.
	agent.NewScheduler(ctx, d.Loop).Restore([]string{"S1"}, time.Now())

	// During the drain the retiring daemon's turn cancels s1 and books s2
	// for a moment from now, then lets go.
	at := time.Now().Add(2 * time.Second)
	appendOnDisk(t, dir, "S1",
		events.Event{Seq: 2, Type: events.TypeScheduleRemoved, Data: map[string]any{"id": "s1"}},
		events.Event{Seq: 3, Type: events.TypeScheduleCreated, Data: map[string]any{
			"id": "s2", "at": at.Format(time.RFC3339),
			"prompt": "the booked prompt", "agent": "general-purpose",
		}})
	if err := os.Remove(filepath.Join(dir, handoffFile)); err != nil {
		t.Fatal(err)
	}
	if err := d.claimSession("S1"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/S1/schedules", nil))
	var listed struct {
		Schedules []agent.Scheduled `json:"schedules"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	var ids []string
	for _, e := range listed.Schedules {
		ids = append(ids, e.ID+":"+e.Status)
	}
	if len(ids) != 1 || ids[0] != "s2:"+agent.SchedulePending {
		t.Fatalf("after taking S1 over its books hold %v, want only s2 pending: %s", ids, rec.Body.String())
	}

	waitForAsk(t, asked, "the booked prompt")
	for _, req := range asked() {
		if strings.Contains(req, "the cancelled prompt") {
			t.Error("a booking the retiring daemon cancelled still fired here")
		}
	}
	// And the next booking here does not reuse an id the log has used.
	e, err := d.Loop.Schedules.Add("S1", "general-purpose", "later", time.Now().Add(time.Hour), agent.RepeatOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != "s3" {
		t.Errorf("the first booking after the takeover got id %s, want s3", e.ID)
	}
}

// asksAgain reports whether a permission check in sessionID would put a
// question in front of a person, rather than being answered by a grant.
// A question blocks until it is answered, so it is bounded and denied.
func asksAgain(t *testing.T, d *Daemon, sessionID string, ask tools.Ask) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(agent.WithSessionID(context.Background(), sessionID), 300*time.Millisecond)
	defer cancel()
	done := make(chan bool, 1)
	go func() {
		allowed, err := d.Broker.Func()(ctx, ask)
		done <- allowed && err == nil
	}()
	select {
	case allowed := <-done:
		return !allowed
	case <-time.After(mustHappen):
		t.Fatal("the permission check never returned")
		return true
	}
}

// An "allow for this session" answered in the retiring daemon holds once
// the session is taken over, even when this daemon had already read the
// session's grants from its own older copy. Opening the session's
// permissions during the drain did that, and the grants were read once,
// so the next "make test" asked again.
func TestASessionGrantAnsweredDuringTheDrainHoldsAfterTakeover(t *testing.T) {
	dir := t.TempDir()
	old, err := session.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.CreateSession("S1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	// The retiring daemon's question, already in the log this daemon
	// loads, and not yet answered.
	old.Append("S1", events.TypePermissionRequest, map[string]any{
		"id": "old-p1", "tool": config.BashToolName, "description": "make test",
		"rule": config.PermissionRuleFor(config.BashToolName, "make test").Match,
		"line": "make test",
	})
	old.Close()
	writeManifest(t, dir, "S1")
	d, _ := daemonOver(t, dir)

	// A client opens S1's permissions during the drain.
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/S1/permissions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("permissions = %d %s", rec.Code, rec.Body.String())
	}
	// The question is answered in the retiring daemon, which lets go.
	appendOnDisk(t, dir, "S1", events.Event{Seq: 2, Type: events.TypePermissionResolved,
		Data: map[string]any{"id": "old-p1", "allow": true, "scope": agent.ScopeSession}})
	if err := os.Remove(filepath.Join(dir, handoffFile)); err != nil {
		t.Fatal(err)
	}
	if err := d.claimSession("S1"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	if asksAgain(t, d, "S1", tools.Ask{Tool: config.BashToolName, Subject: "make test", Description: "again"}) {
		t.Error("an \"allow for this session\" answered during the drain was lost by the takeover")
	}
}

// A takeover whose re-read fails is tried again by the next write. The
// session used to be marked taken over before it was read, so after one
// failure every later write went ahead on the copy the read was meant to
// replace.
func TestAFailedTakeoverIsTriedAgain(t *testing.T) {
	dir := staleStore(t, []string{"S1"})
	writeManifest(t, dir, "S1")
	d, store := daemonOver(t, dir)
	appendOnDisk(t, dir, "S1", events.Event{Seq: 3, Type: events.TypeUserMessage, Data: map[string]any{"text": "from the drain"}})
	if err := os.Remove(filepath.Join(dir, handoffFile)); err != nil {
		t.Fatal(err)
	}

	meta := filepath.Join(dir, "S1.meta.json")
	if err := os.Rename(meta, meta+".away"); err != nil {
		t.Fatal(err)
	}
	if err := d.claimSession("S1"); err == nil {
		t.Fatal("a takeover that could not read the session reported success")
	}
	if err := os.Rename(meta+".away", meta); err != nil {
		t.Fatal(err)
	}
	if err := d.claimSession("S1"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if evs, _ := store.Events("S1", 0); len(evs) != 3 {
		t.Errorf("after a failed takeover and a second write S1 holds %d events, want the 3 on disk", len(evs))
	}
}

// A takeover whose re-read panics (net/http recovers a handler's panic and
// the daemon goes on) neither leaves the writes after it waiting for good
// nor counts as done: the next write takes the session over properly.
func TestATakeoverThatPanicsIsTriedAgain(t *testing.T) {
	dir := staleStore(t, []string{"S1"})
	writeManifest(t, dir, "S1")
	d, _ := daemonOver(t, dir)
	// Read from the old copy during the drain, so only a takeover that
	// finishes finds what the retiring daemon wrote afterwards.
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/S1/permissions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("permissions = %d %s", rec.Code, rec.Body.String())
	}
	appendOnDisk(t, dir, "S1",
		events.Event{Seq: 3, Type: events.TypePermissionRequest, Data: map[string]any{
			"id": "drain-p1", "tool": config.BashToolName, "description": "make test",
			"rule": config.PermissionRuleFor(config.BashToolName, "make test").Match,
			"line": "make test",
		}},
		events.Event{Seq: 4, Type: events.TypePermissionResolved,
			Data: map[string]any{"id": "drain-p1", "allow": true, "scope": agent.ScopeSession}})
	if err := os.Remove(filepath.Join(dir, handoffFile)); err != nil {
		t.Fatal(err)
	}

	takeoverBarrier = func() { panic("the re-read went wrong") }
	t.Cleanup(func() { takeoverBarrier = nil })
	func() {
		defer func() { _ = recover() }()
		_ = d.claimSession("S1")
		t.Error("the panic did not reach the caller")
	}()
	takeoverBarrier = nil

	claimed := make(chan error, 1)
	go func() { claimed <- d.claimSession("S1") }()
	select {
	case err := <-claimed:
		if err != nil {
			t.Fatalf("claim after the panic: %v", err)
		}
	case <-time.After(mustHappen):
		t.Fatal("a write after a takeover that panicked waited for it for good")
	}
	if asksAgain(t, d, "S1", tools.Ask{Tool: config.BashToolName, Subject: "make test", Description: "again"}) {
		t.Error("after a takeover that panicked, the next write did not take S1 over again")
	}
}
