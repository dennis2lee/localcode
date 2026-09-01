package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Reasoning from a local model.
//
// The OpenAI API has no field for it, so every runtime that streams it
// invented one, and this adapter read neither. A model that reasoned and
// then ran tools looked, from the screen, like a model that ran tools
// with nothing to say — which is exactly how it was reported: "it just
// runs tools with no explanation and then says it is done."
//
// The clients already knew what to do with it. Both have had a thinking
// path since the Anthropic adapter got one; there was simply never an
// event for them to receive.

// stream runs one canned SSE body through the adapter and returns the
// events it produced, in order.
func streamOf(t *testing.T, body string) []StreamEvent {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, body)
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	p := NewOpenAICompat(srv.URL, "")
	stream, err := p.Chat(context.Background(), ChatRequest{
		Model: "m", Messages: []Message{{Role: RoleUser, Content: []Block{TextBlock("hi")}}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	var out []StreamEvent
	for ev := range stream {
		out = append(out, ev)
	}
	return out
}

func kinds(evs []StreamEvent) []StreamEventType {
	var out []StreamEventType
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

func thinkingText(evs []StreamEvent) string {
	var b strings.Builder
	for _, e := range evs {
		if e.Type == EventThinkingDelta {
			b.WriteString(e.ThinkingDelta)
		}
	}
	return b.String()
}

// Both spellings, because a server sends one or the other and which one
// is not something the person running it chose. DeepSeek introduced
// reasoning_content and vLLM, SGLang, LM Studio, llama.cpp and Ollama
// followed; OpenRouter and others send reasoning.
func TestReasoningArrivesUnderEitherName(t *testing.T) {
	for _, field := range []string{"reasoning_content", "reasoning"} {
		body := fmt.Sprintf(
			"data: {\"choices\":[{\"delta\":{%q:\"let me check the file\"}}]}\n\n"+
				"data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n"+
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
				"data: [DONE]\n\n", field)
		evs := streamOf(t, body)
		if got := thinkingText(evs); got != "let me check the file" {
			t.Errorf("%s produced thinking %q, want the reasoning text", field, got)
		}
	}
}

// The block has to close, and it has to close before the visible text.
// There is no end marker in this protocol — the field just stops
// appearing — so the first content is the signal.
func TestReasoningClosesBeforeTheAnswer(t *testing.T) {
	evs := streamOf(t, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\"}}]}\n\n"+
		"data: {\"choices\":[{\"delta\":{\"content\":\"the answer\"}}]}\n\n"+
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
		"data: [DONE]\n\n")

	var end, text int = -1, -1
	for i, e := range evs {
		switch e.Type {
		case EventThinkingEnd:
			end = i
		case EventTextDelta:
			if text < 0 {
				text = i
			}
		}
	}
	if end < 0 {
		t.Fatalf("the reasoning block never closed: %v", kinds(evs))
	}
	if text < 0 || end > text {
		t.Errorf("thinking closed at %d and text began at %d; it must close first: %v", end, text, kinds(evs))
	}
}

// The case this was reported for: reasoning and then straight to a tool
// call, with nothing said. The block still has to close, or the status
// line reads "thinking" for the rest of the session.
func TestReasoningClosesBeforeAToolCall(t *testing.T) {
	evs := streamOf(t, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"I should read it\"}}]}\n\n"+
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{}\"}}]}}]}\n\n"+
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"+
		"data: [DONE]\n\n")

	seen := kinds(evs)
	end, start := -1, -1
	for i, k := range seen {
		if k == EventThinkingEnd && end < 0 {
			end = i
		}
		if k == EventToolUseStart && start < 0 {
			start = i
		}
	}
	if end < 0 {
		t.Fatalf("reasoning followed by a tool call never closed the block: %v", seen)
	}
	if start < 0 || end > start {
		t.Errorf("thinking closed at %d, the tool started at %d: %v", end, start, seen)
	}
}

// A reply that reasons and then stops without saying anything. Rare, and
// the cost of missing it is a status line stuck on "thinking".
func TestReasoningAloneStillCloses(t *testing.T) {
	for _, body := range []string{
		"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"hm\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
		// And a server that just closes, which local runtimes do.
		"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"hm\"}}]}\n\n",
	} {
		evs := streamOf(t, body)
		closed := false
		for _, e := range evs {
			if e.Type == EventThinkingEnd {
				closed = true
			}
		}
		if !closed {
			t.Errorf("reasoning with no answer never closed: %v", kinds(evs))
		}
	}
}

// A reply with no reasoning must be byte-identical to what it was. The
// overwhelming majority of servers send none, and a stray thinking event
// would put an empty block in front of every answer they give.
func TestAReplyWithNoReasoningIsUnchanged(t *testing.T) {
	evs := streamOf(t, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"+
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
		"data: [DONE]\n\n")
	for _, e := range evs {
		if e.Type == EventThinkingDelta || e.Type == EventThinkingEnd {
			t.Errorf("a reply with no reasoning produced %s", e.Type)
		}
	}
}

// Reasoning is watched and then forgotten. It is never sent back, and the
// reason it is never sent back is that toOpenAIMessages has no case for a
// thinking block — a silent drop that this pins, because a later default
// branch that started serialising them would put a field on the way back
// in that most servers reject.
func TestAThinkingBlockIsNeverSentBack(t *testing.T) {
	msgs := toOpenAIMessages("", []Message{{
		Role: RoleAssistant,
		Content: []Block{
			{Type: BlockThinking, Text: "the model's private reasoning"},
			TextBlock("the answer"),
		},
	}})
	for _, m := range msgs {
		if strings.Contains(m.Content, "private reasoning") {
			t.Fatalf("a thinking block was serialised back into the request: %+v", m)
		}
	}
	if len(msgs) != 1 || msgs[0].Content != "the answer" {
		t.Errorf("messages = %+v, want just the answer", msgs)
	}
}
