package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// The daemon ends a stream that has fallen behind rather than skipping
// events on it, because every event is a piece of the model's reply and a
// skipped one is gone for good. That only helps if the client comes back —
// and comes back at the right place. A client that treats the end of the
// stream as the end of the conversation shows a reply that stops halfway
// and a turn that never finishes, which is the bug this pair exists to
// prevent.
func TestStreamEventsResumesWhereItLeftOff(t *testing.T) {
	var connections int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&connections, 1)
		since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		// First connection delivers 1 and 2 and then ends, standing in for
		// a stream the daemon dropped mid-turn. The second must ask to
		// resume from 2, and gets the rest.
		var send []uint64
		switch {
		case n == 1:
			send = []uint64{1, 2}
		case since == 2:
			send = []uint64{3, 4}
		default:
			t.Errorf("reconnect asked for since=%d, want 2 — it will either miss events or replay ones already shown", since)
		}
		for _, seq := range send {
			fmt.Fprintf(w, "id: %d\ndata: {\"seq\":%d,\"type\":\"message.part.delta\",\"data\":{\"text\":\"x\"}}\n\n", seq, seq)
			flusher.Flush()
		}
		if n > 1 {
			// Hold the second connection open so the test sees exactly
			// four events rather than an endless replay.
			<-r.Context().Done()
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ch := New(srv.URL).StreamEvents(ctx, "s1", 0)

	var got []uint64
	for len(got) < 4 {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed after %v; it should have reconnected", got)
			}
			got = append(got, ev.Seq)
		case <-ctx.Done():
			t.Fatalf("only got %v before timing out", got)
		}
	}

	for i, want := range []uint64{1, 2, 3, 4} {
		if got[i] != want {
			t.Fatalf("events = %v, want [1 2 3 4] — no gap, no duplicate", got)
		}
	}
	if atomic.LoadInt32(&connections) < 2 {
		t.Error("never reconnected")
	}
}
