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

	"localcode/internal/events"
	"localcode/internal/session"
)

// A session's log is the record, and a client attaching to it is entitled
// to all of it.
//
// The backlog used to be filtered a second time on the way out, against a
// running maximum: an event whose sequence failed to exceed the one before
// it was dropped. On an ordinary log that never fires. On a log whose
// sequences repeat or go backwards it silently deletes conversation from
// every attach, in every client, forever — and such a log is producible,
// because two daemons sharing one session directory keep independent
// counters and their appends interleave.
func TestADamagedSequenceDoesNotCostTheRecord(t *testing.T) {
	dir := t.TempDir()
	const id = "s-damaged"
	writeSessionLog(t, dir, id, []events.Event{
		logEvent(1, events.TypeUserMessage, "first question"),
		logEvent(2, events.TypeMessagePartEnd, "first answer"),
		// The second daemon's counter, which started over.
		logEvent(1, events.TypeUserMessage, "second question"),
		logEvent(2, events.TypeMessagePartEnd, "second answer"),
		// And back to the first daemon's, still climbing.
		logEvent(3, events.TypeUserMessage, "third question"),
	})

	body := attachAndRead(t, dir, id)
	for _, want := range []string{
		"first question", "first answer",
		"second question", "second answer",
		"third question",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the replay lost %q — it is in the log and no client will ever show it:\n%s", want, body)
		}
	}
}

// The resume point is what the browser sends back after a dropped
// connection, so it must never move backwards: a repeated sequence in the
// log would otherwise rewind it and re-deliver everything after it.
func TestTheResumeIDNeverGoesBackwards(t *testing.T) {
	dir := t.TempDir()
	const id = "s-resume"
	writeSessionLog(t, dir, id, []events.Event{
		logEvent(1, events.TypeUserMessage, "a"),
		logEvent(7, events.TypeUserMessage, "b"),
		logEvent(2, events.TypeUserMessage, "c"),
	})

	var ids []string
	for _, line := range strings.Split(attachAndRead(t, dir, id), "\n") {
		if rest, ok := strings.CutPrefix(line, "id: "); ok {
			ids = append(ids, rest)
		}
	}
	if len(ids) < 3 {
		t.Fatalf("saw %d id lines, want one per event: %v", len(ids), ids)
	}
	if ids[0] != "1" || ids[1] != "7" || ids[2] != "7" {
		t.Errorf("resume ids = %v, want 1, 7, 7 — the third event's own 2 would send a reconnect back past b", ids)
	}
}

func logEvent(seq uint64, typ events.Type, text string) events.Event {
	return events.Event{
		Seq:       seq,
		Type:      typ,
		Timestamp: time.Unix(0, 0).UTC(),
		Data:      map[string]any{"text": text},
	}
}

// writeSessionLog lays a session down on disk directly, which is the only
// way to produce a log the append path would never write.
func writeSessionLog(t *testing.T, dir, id string, evs []events.Event) {
	t.Helper()
	meta := fmt.Sprintf(`{"id":%q,"visible":true,"agent":"general-purpose","created_at":"2026-01-01T00:00:00Z"}`, id)
	if err := os.WriteFile(filepath.Join(dir, id+".meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, ev := range evs {
		ev.Session = id
		line, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// attachAndRead opens the event stream for a session restored from dir and
// returns what the daemon wrote before the backlog ran out.
func attachAndRead(t *testing.T, dir, id string) string {
	t.Helper()
	// The backlog is written before the stream blocks, and the heartbeat
	// is what marks that point, so a short one is what makes this quick
	// rather than a wait for the context to expire.
	prev := heartbeatInterval
	heartbeatInterval = 30 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = prev })

	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	store, warnings, err := session.LoadAllFromDisk(dir)
	if err != nil {
		t.Fatalf("load sessions: %v", err)
	}
	for _, w := range warnings {
		t.Logf("restore warning: %v", w)
	}

	d := newTestDaemon(t, model.URL)
	d.Loop.Store = store
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/sessions/"+id+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()

	// The backlog is written before anything blocks, so reading until the
	// stream goes quiet is enough. The heartbeat marks that point.
	var out strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, ":") {
			break
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}
