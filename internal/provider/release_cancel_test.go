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

// An OpenAI-compatible server that says how much of the prompt its cache
// served (OpenAI does, and vLLM with prefix caching) has that part
// reported as a cache read, apart from the fresh input, as Anthropic and
// Bedrock report theirs. prompt_tokens includes it, so reported whole
// the cache counted as fresh input in /usage; the whole prompt, which is
// what the window reads, is the same either way.
func TestAnOpenAICompatCacheReadIsReportedApart(t *testing.T) {
	for _, c := range []struct {
		name                 string
		usage                string
		input, cached, total int
	}{
		{"a cache hit", `{"prompt_tokens":5000,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":4096}}`, 904, 4096, 5000},
		{"no details", `{"prompt_tokens":5000,"completion_tokens":7}`, 5000, 0, 5000},
		{"a cache larger than the prompt", `{"prompt_tokens":100,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":4096}}`, 0, 100, 100},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
				fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":%s}\n\n", c.usage)
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer srv.Close()
			stream, err := NewOpenAICompat(srv.URL, "").Chat(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: RoleUser, Content: []Block{TextBlock("hi")}}}})
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}
			var got *StreamEvent
			for ev := range stream {
				if ev.Type == EventUsage {
					e := ev
					got = &e
				}
			}
			if got == nil {
				t.Fatal("no usage event")
			}
			if got.InputTokens != c.input || got.CacheReadTokens != c.cached || got.CacheWriteTokens != 0 || got.InputTokens+got.CacheReadTokens != c.total {
				t.Errorf("usage = input %d, cache read %d, cache write %d; want input %d, cache read %d, whole prompt %d",
					got.InputTokens, got.CacheReadTokens, got.CacheWriteTokens, c.input, c.cached, c.total)
			}
		})
	}
}
