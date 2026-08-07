package daemon

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"localcode/internal/events"
)

// A turn can run for minutes without producing an event, and in that gap
// nothing crosses the connection at all. Anything in the middle — a
// proxy, a corporate network appliance, an OS idle timeout — is then free
// to drop it, and the browser is not always told: EventSource sits there
// believing it is connected, no error fires, no reconnect happens, and
// every event after that is lost.
//
// What the user sees is a light blinking forever, no output, and a stop
// button that appears to do nothing — the daemon does cancel, and it is
// the reply that never arrives.
//
// The heartbeat is what gives both ends something to notice a dead
// connection by. This asserts it actually reaches the wire during
// silence, which is the only case that matters.
func TestEventStreamHeartbeatsWhileNothingHappens(t *testing.T) {
	prev := heartbeatInterval
	heartbeatInterval = 30 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = prev })

	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	sess, err := d.Loop.Store.CreateSession("s-heartbeat-"+t.Name(), "", "general-purpose", true)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/sessions/"+sess.ID+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()

	// No events are produced at any point here: the session is idle, which
	// is exactly the state a long-running turn leaves the stream in.
	beats := 0
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() && beats < 2 {
		if strings.HasPrefix(scanner.Text(), ":") {
			beats++
		}
	}
	if beats < 2 {
		t.Fatalf("saw %d heartbeats on an idle stream, want at least 2 — a silent connection has nothing to notice a drop by", beats)
	}
}

// A comment line must not be mistaken for an event, and must not disturb
// the Last-Event-ID a client resumes from.
func TestHeartbeatCarriesNoEventID(t *testing.T) {
	prev := heartbeatInterval
	heartbeatInterval = 30 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = prev })

	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	sess, err := d.Loop.Store.CreateSession("s-heartbeat-"+t.Name(), "", "general-purpose", true)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/sessions/"+sess.ID+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, ":") {
			if line != ": ping" {
				t.Errorf("heartbeat line = %q, want a bare SSE comment", line)
			}
			return
		}
		if strings.HasPrefix(line, "id:") || strings.HasPrefix(line, "data:") {
			t.Fatalf("an idle stream emitted %q instead of a heartbeat", line)
		}
	}
	t.Fatal("stream closed without a heartbeat")
}

// A transient event (Store.Broadcast) is true only at the moment it is
// sent — the live tokens-per-second readout is what this exists for. It
// must reach an attached client, and it must not claim a place in the log:
// an `id:` line on one would become the Last-Event-ID a browser resumes
// from, and the daemon would then replay the real session log from the
// wrong point, silently dropping everything the person had not seen yet.
func TestTransientEventsCarryNoEventID(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	sess, err := d.Loop.Store.CreateSession("s-transient-"+t.Name(), "", "general-purpose", true)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/sessions/"+sess.ID+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()

	// Broadcast until one lands: the subscription is established inside the
	// handler, so the first attempt can beat it there.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			d.Loop.Store.Broadcast(sess.ID, events.TypeUsage, map[string]any{"tps": 12.5, "estimated": true})
			time.Sleep(20 * time.Millisecond)
		}
	}()
	defer func() { cancel(); <-done }()

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "id:") {
			t.Fatalf("a transient event was given a sequence id: %q", line)
		}
		if strings.HasPrefix(line, "data:") && strings.Contains(line, `"tps":12.5`) {
			break // arrived, with no id: line before it
		}
	}

	// And it left nothing behind in the log.
	logged, err := d.Loop.Store.Events(sess.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range logged {
		if ev.Type == events.TypeUsage {
			t.Fatalf("a transient event was recorded in the session log: %+v", ev)
		}
	}
}
