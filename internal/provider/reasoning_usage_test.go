package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The part of completion_tokens a reasoning model spent thinking, where
// the server says: kept apart, so the context measures can leave out what
// is never sent back. Held to the output it is part of.
func TestAnOpenAICompatReasoningCountIsReportedApart(t *testing.T) {
	for _, c := range []struct {
		name      string
		usage     string
		output    int
		reasoning int
	}{
		{"reasoning reported", `{"prompt_tokens":100,"completion_tokens":900,"completion_tokens_details":{"reasoning_tokens":850}}`, 900, 850},
		{"no details", `{"prompt_tokens":100,"completion_tokens":900}`, 900, 0},
		{"more reasoning than output", `{"prompt_tokens":100,"completion_tokens":50,"completion_tokens_details":{"reasoning_tokens":400}}`, 50, 50},
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
			if got.OutputTokens != c.output || got.ReasoningTokens != c.reasoning {
				t.Errorf("output %d, reasoning %d; want %d and %d", got.OutputTokens, got.ReasoningTokens, c.output, c.reasoning)
			}
		})
	}
}
