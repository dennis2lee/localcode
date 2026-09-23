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
