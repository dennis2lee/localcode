package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"localcode/internal/client"
	"localcode/internal/events"
)

// firstSeqs opens the session's stream with the given query and header and
// returns the seqs of the first n logged events it replays.
func firstSeqs(t *testing.T, srvURL, sessionID, query, lastEventID string, n int) []float64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srvURL+"/api/sessions/"+sessionID+"/events"+query, nil)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()
	var seqs []float64
	scanner := bufio.NewScanner(resp.Body)
	for len(seqs) < n && scanner.Scan() {
		line, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Seq float64 `json:"seq"`
		}
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Seq > 0 {
			seqs = append(seqs, ev.Seq)
		}
	}
	return seqs
}

// A browser that reconnects a stream it opened with ?since= resends the
// same URL with a Last-Event-ID, and the Last-Event-ID is where the stream
// got to. Preferring the ?since= replayed everything the page had drawn
// since it opened the stream, which drew all of it a second time.
func TestLastEventIDBeatsSinceOnAReconnect(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()
	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	sess, err := d.Loop.Store.CreateSession("s-resume-"+t.Name(), "", "general-purpose", true)
	if err != nil {
		t.Fatal(err)
	}
	var seqs []uint64
	for i := 0; i < 5; i++ {
		ev, err := d.Loop.Store.Append(sess.ID, events.TypeMessagePartEnd, map[string]any{"text": "x"})
		if err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, ev.Seq)
	}
	since := "?since=" + itoa(seqs[0])
	got := firstSeqs(t, srv.URL, sess.ID, since, itoa(seqs[2]), 1)
	if len(got) != 1 || uint64(got[0]) != seqs[3] {
		t.Errorf("with ?since=%d and Last-Event-ID %d the replay starts at %v, want %d", seqs[0], seqs[2], got, seqs[3])
	}
	// Without the header, ?since= decides, as the Go client relies on.
	got = firstSeqs(t, srv.URL, sess.ID, since, "", 1)
	if len(got) != 1 || uint64(got[0]) != seqs[1] {
		t.Errorf("with ?since=%d alone the replay starts at %v, want %d", seqs[0], got, seqs[1])
	}
}

func itoa(n uint64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// A logged reasoning block comes back on a replay, which is what lets a
// client that reloads or reconnects draw it again.
func TestALoggedReasoningBlockIsReplayed(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()
	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	sess, err := d.Loop.Store.CreateSession("s-replay-"+t.Name(), "", "general-purpose", true)
	if err != nil {
		t.Fatal(err)
	}
	d.Loop.Store.Append(sess.ID, events.TypeThinkingBlock, map[string]any{"text": "kept", "elapsed_ms": 1200})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/sessions/"+sess.ID+"/events?tail=400", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if line, ok := strings.CutPrefix(scanner.Text(), "data: "); ok && strings.Contains(line, `"thinking.block"`) {
			if !strings.Contains(line, `"text":"kept"`) {
				t.Errorf("replayed block = %s", line)
			}
			return
		}
	}
	t.Fatal("the logged block was not replayed")
}

// During an update's handoff the old daemon goes on finishing its turns,
// and the new one lists those sessions as busy. It listed them idle, and a
// client whose stream had reconnected to the new daemon was told its turn
// was lost while the old one was still writing the answer.
func TestASessionTheOldDaemonIsFinishingIsListedBusy(t *testing.T) {
	d, store, dir := handoffDaemon(t)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	for _, id := range []string{"S1", "S2"} {
		if _, err := store.CreateSession(id, "", "general-purpose", true); err != nil {
			t.Fatal(err)
		}
	}
	writeManifest(t, dir, "S1")
	resp, err := http.Get(srv.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var listed []struct {
		ID   string `json:"id"`
		Busy bool   `json:"busy"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	busy := map[string]bool{}
	for _, s := range listed {
		busy[s.ID] = s.Busy
	}
	if !busy["S1"] {
		t.Errorf("the session the old daemon owns is listed idle: %v", listed)
	}
	if busy["S2"] {
		t.Errorf("a session nobody is running is listed busy: %v", listed)
	}
}

// An invalid ?since= or ?tail= is refused whatever header came with it.
func TestAnInvalidQueryIsRefusedEvenWithALastEventID(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()
	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	sess, err := d.Loop.Store.CreateSession("s-bad-"+t.Name(), "", "general-purpose", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"?since=abc", "?tail=-1", "?tail=x"} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/sessions/"+sess.ID+"/events"+q, nil)
		req.Header.Set("Last-Event-ID", "1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s with a Last-Event-ID answered %d, want 400", q, resp.StatusCode)
		}
	}
}

// When the daemon this one took over from lets go of a session, this one
// re-reads it at once and says so: the list stops calling it busy, every
// client hears session.activity {busy:false}, and a stream open on it
// gets what the old daemon wrote after this one loaded it, the turn's end
// included.
func TestAReleasedSessionIsReReadAndAnnouncedIdle(t *testing.T) {
	prev := takeoverPoll
	takeoverPoll = 20 * time.Millisecond
	t.Cleanup(func() { takeoverPoll = prev })

	d, store, dir := handoffDaemon(t)
	for _, id := range []string{"S1", "S2"} {
		if _, err := store.CreateSession(id, "", "general-purpose", true); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.Append("S1", events.TypeUserMessage, map[string]any{"text": "go"})
	if err != nil {
		t.Fatal(err)
	}
	writeManifest(t, dir, "S1")
	d.NoteTakeover()
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := client.New(srv.URL)
	s1 := c.StreamEvents(ctx, "S1", first.Seq)
	daemonWide, err := c.SubscribeEvents(ctx, "S2", 0)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)

	// The old daemon finishes the turn, on disk, and lets go.
	f, err := os.OpenFile(filepath.Join(dir, "S1.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	for i, typ := range []events.Type{events.TypeMessagePartEnd, events.TypeTurnDone} {
		line, _ := json.Marshal(events.Event{Seq: first.Seq + uint64(i) + 1, Session: "S1", Type: typ, Timestamp: time.Now(), Data: map[string]any{"text": "the old daemon's answer"}})
		f.Write(append(line, '\n'))
	}
	f.Close()
	if err := os.Remove(filepath.Join(dir, handoffFile)); err != nil {
		t.Fatal(err)
	}

	gotDone, gotIdle := false, false
	for !gotDone || !gotIdle {
		select {
		case ev := <-s1:
			if ev.Type == events.TypeTurnDone {
				gotDone = true
			}
		case ev := <-daemonWide:
			if ev.Type == events.TypeSessionActivity && ev.Data["session"] == "S1" && ev.Data["busy"] == false {
				gotIdle = true
			}
		case <-ctx.Done():
			t.Fatalf("after the release: turn.done delivered %v, idle announced %v", gotDone, gotIdle)
		}
	}
	resp, err := http.Get(srv.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var listed []struct {
		ID   string `json:"id"`
		Busy bool   `json:"busy"`
	}
	json.NewDecoder(resp.Body).Decode(&listed)
	for _, s := range listed {
		if s.ID == "S1" && s.Busy {
			t.Error("the released session is still listed busy")
		}
	}
}
