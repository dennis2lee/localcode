package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"localcode/internal/events"
)

// droppingServer serves 1 and 2, ends the stream, then serves 3 and 4 on
// the next connection and holds it.
func droppingServer(t *testing.T) *httptest.Server {
	t.Helper()
	var connections int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&connections, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		send := []uint64{1, 2}
		if n > 1 {
			send = []uint64{3, 4}
		}
		for _, seq := range send {
			fmt.Fprintf(w, "id: %d\ndata: {\"seq\":%d,\"type\":\"message.part.delta\",\"data\":{\"text\":\"x\"}}\n\n", seq, seq)
			w.(http.Flusher).Flush()
		}
		if n > 1 {
			<-r.Context().Done()
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func collect(t *testing.T, ch <-chan events.Event, n int) []events.Event {
	t.Helper()
	var got []events.Event
	timeout := time.After(10 * time.Second)
	for len(got) < n {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed after %v", got)
			}
			got = append(got, ev)
		case <-timeout:
			t.Fatalf("timed out after %v", got)
		}
	}
	return got
}

// The marking stream says when it came back, in order: after what the
// first connection delivered and before what the second brings. The
// first connection is not a reconnect.
func TestTheMarkingStreamSaysWhenItCameBack(t *testing.T) {
	srv := droppingServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := collect(t, New(srv.URL).StreamEventsMarkingReconnects(ctx, "s1", 0), 5)
	var shape []string
	for _, ev := range got {
		if ev.Type == TypeReconnected {
			if ev.Seq != 0 {
				t.Errorf("the marker has seq %d, want 0 so no resume point moves", ev.Seq)
			}
			shape = append(shape, "marker")
			continue
		}
		shape = append(shape, fmt.Sprint(ev.Seq))
	}
	if fmt.Sprint(shape) != "[1 2 marker 3 4]" {
		t.Errorf("stream = %v, want [1 2 marker 3 4]", shape)
	}
}

// The plain stream never carries the marker: localcode run prints every
// event it gets.
func TestThePlainStreamCarriesNoMarker(t *testing.T) {
	srv := droppingServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, ev := range collect(t, New(srv.URL).StreamEvents(ctx, "s1", 0), 4) {
		if ev.Type == TypeReconnected {
			t.Fatal("the plain stream delivered a reconnect marker")
		}
	}
}

// SessionBusy reads the flag off the session list, and says when the
// session is not in it.
func TestSessionBusyReadsTheList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":"a","busy":true},{"id":"b","busy":false}]`)
	}))
	defer srv.Close()
	c := New(srv.URL)
	for _, tc := range []struct {
		id          string
		busy, found bool
	}{{"a", true, true}, {"b", false, true}, {"gone", false, false}} {
		busy, found, err := c.SessionBusy(context.Background(), tc.id)
		if err != nil || busy != tc.busy || found != tc.found {
			t.Errorf("SessionBusy(%q) = %v, %v, %v; want %v, %v", tc.id, busy, found, err, tc.busy, tc.found)
		}
	}
}

// show_thinking is read as a pointer: a daemon without the key leaves it
// nil, which a client reads as its default, rather than as off.
func TestShowThinkingIsNilWhenTheDaemonDoesNotSay(t *testing.T) {
	for body, want := range map[string]string{
		`{"show_tps":true}`:                       "nil",
		`{"show_tps":true,"show_thinking":false}`: "false",
		`{"show_thinking":true}`:                  "true",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, body)
		}))
		s, err := New(srv.URL).GetSettings(context.Background())
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		got := "nil"
		if s.ShowThinking != nil {
			got = fmt.Sprint(*s.ShowThinking)
		}
		if got != want {
			t.Errorf("%s: ShowThinking = %s, want %s", body, got, want)
		}
	}
}
