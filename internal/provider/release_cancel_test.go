package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A cancelled turn closes the connection and the read fails with the
// cancel. The readers offered that as a stream error, and their send
// chose at random between the event channel and the context's done
// channel, so about a third of cancels arrived as errors: the reply was
// closed as failed, an error line was drawn, and the reply the other
// two thirds kept was dropped.

func TestAReadThatFailsAfterACancelIsTheCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	boom := errors.New("connection reset")
	if err := readError(ctx, boom); err != boom {
		t.Errorf("a read failure on a live turn = %v, want the failure", err)
	}
	if err := readError(ctx, nil); err != nil {
		t.Errorf("a clean end on a live turn = %v, want nil", err)
	}
	cancel()
	if err := readError(ctx, boom); err != nil {
		t.Errorf("a read failure after the cancel = %v, want nothing: the cancel is the reason", err)
	}
}

// hangingSSE streams the start of a reply and then holds the connection
// open until the client goes away, which is what a cancel does to it.
func hangingSSE(t *testing.T, lines ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, l := range lines {
			fmt.Fprint(w, l)
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// cancelsEndCleanly cancels forty streams at their first text and counts
// the ones that ended in an error event. One is one too many: the turn
// loop reads a clean close as a cancelled reply to keep, and an error as
// a failed reply to drop.
func cancelsEndCleanly(t *testing.T, p Provider) {
	t.Helper()
	const runs = 40
	errored := 0
	var sample error
	for i := 0; i < runs; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		stream, err := p.Chat(ctx, ChatRequest{
			Model:     "m",
			Messages:  []Message{{Role: RoleUser, Content: []Block{TextBlock("hi")}}},
			MaxTokens: 10,
		})
		if err != nil {
			cancel()
			t.Fatalf("Chat: %v", err)
		}
		for ev := range stream {
			if ev.Type == EventTextDelta {
				cancel()
			}
			if ev.Type == EventError {
				errored++
				sample = ev.Err
			}
		}
		cancel()
	}
	if errored > 0 {
		t.Errorf("%d of %d cancelled streams ended in an error event (e.g. %v); a cancel is a clean close", errored, runs, sample)
	}
}

func TestACancelledAnthropicStreamEndsCleanly(t *testing.T) {
	srv := hangingSSE(t,
		sseLine(map[string]any{"type": "message_start", "message": map[string]any{"usage": map[string]any{"input_tokens": 1}}}),
		sseLine(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text"}}),
		sseLine(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": "half an"}}),
	)
	p := NewAnthropicDirect("k")
	p.BaseURL = srv.URL
	cancelsEndCleanly(t, p)
}

func TestACancelledOpenAICompatStreamEndsCleanly(t *testing.T) {
	srv := hangingSSE(t, "data: {\"choices\":[{\"delta\":{\"content\":\"half an\"}}]}\n\n")
	cancelsEndCleanly(t, NewOpenAICompat(srv.URL, ""))
}
