package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// splitAll runs pieces through one inlineThink and returns the reasoning
// and the answer it separated.
func splitAll(pieces []string) (string, string) {
	var t inlineThink
	var r, a strings.Builder
	for _, p := range pieces {
		x, y := t.feed(p)
		r.WriteString(x)
		a.WriteString(y)
	}
	x, y := t.flush()
	r.WriteString(x)
	a.WriteString(y)
	return r.String(), a.String()
}

// Wherever the server cuts its deltas, the reasoning and the answer come
// out the same: every single cut point and every pair of cut points of a
// real-shaped reply, tags split through the middle included.
func TestInlineThinkSplitsWhereverTheDeltasAreCut(t *testing.T) {
	const reply = "  <think>\nThe user asks 17 × 23. 17 × 20 = 340.</think>\n\n17 × 23 = **391**."
	const wantR, wantA = "The user asks 17 × 23. 17 × 20 = 340.", "17 × 23 = **391**."
	cuts := []int{}
	for i := 1; i < len(reply); i++ {
		if isRuneStart(reply[i]) {
			cuts = append(cuts, i)
		}
	}
	check := func(pieces []string) {
		t.Helper()
		if r, a := splitAll(pieces); r != wantR || a != wantA {
			t.Fatalf("pieces %q gave reasoning %q and answer %q", pieces, r, a)
		}
	}
	check([]string{reply})
	for _, i := range cuts {
		check([]string{reply[:i], reply[i:]})
		for _, j := range cuts {
			if j > i {
				check([]string{reply[:i], reply[i:j], reply[j:]})
			}
		}
	}
	var perRune []string
	for _, r := range reply {
		perRune = append(perRune, string(r))
	}
	check(perRune)
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// What is not a leading think block passes through as it came.
func TestInlineThinkLeavesOtherAnswersAlone(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pieces    []string
		reasoning string
		answer    string
	}{
		{"a plain answer, leading whitespace kept", []string{"\n  Hello", " there"}, "", "\n  Hello there"},
		{"the tag quoted later in an answer", []string{"Use a <think> tag", " like </think> this"}, "", "Use a <think> tag like </think> this"},
		{"a longer tag is not the tag", []string{"<thinking>no</thinking>"}, "", "<thinking>no</thinking>"},
		{"a start that never becomes a tag", []string{"<thi"}, "", "<thi"},
		{"whitespace only", []string{" \n "}, "", ""},
		{"an empty think block", []string{"<think>\n\n</think>\n\nanswer"}, "", "answer"},
		{"a block never closed is reasoning to the end", []string{"<think>still going", " <"}, "still going <", ""},
		{"a second think block is answer", []string{"<think>a</think>b<think>c</think>"}, "a", "b<think>c</think>"},
		{"Korean reasoning", []string{"<think>사용자가 ", "묻는다</thi", "nk>답"}, "사용자가 묻는다", "답"},
		{"a bare opening tag and nothing after", []string{"<think>"}, "", ""},
		{"a bare opening tag, one byte at a time", []string{"<", "t", "h", "i", "n", "k", ">"}, "", ""},
		{"whitespace, then a bare opening tag", []string{"  <think>"}, "", ""},
	} {
		if r, a := splitAll(tc.pieces); r != tc.reasoning || a != tc.answer {
			t.Errorf("%s: reasoning %q answer %q, want %q and %q", tc.name, r, a, tc.reasoning, tc.answer)
		}
	}
}

func contentChunks(pieces ...string) string {
	var b strings.Builder
	for _, p := range pieces {
		raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": p}}}})
		fmt.Fprintf(&b, "data: %s\n\n", raw)
	}
	return b.String()
}

func textOf(evs []StreamEvent) string {
	var b strings.Builder
	for _, e := range evs {
		if e.Type == EventTextDelta {
			b.WriteString(e.TextDelta)
		}
	}
	return b.String()
}

// A server that puts the reasoning in the answer, as LM Studio does with
// its separate-reasoning setting off: the adapter reports it as
// reasoning, closes it before the answer's first text, and the answer
// carries neither the tags nor the reasoning.
func TestReasoningInsideTheAnswerIsReportedAsReasoning(t *testing.T) {
	body := contentChunks("<think>", "The user asks 17 times 23.", " That is 391.</thi", "nk>\n\n", "17 times 23 is 391.") +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	evs := streamOf(t, body)
	if got := thinkingText(evs); got != "The user asks 17 times 23. That is 391." {
		t.Errorf("reasoning = %q", got)
	}
	if got := textOf(evs); got != "17 times 23 is 391." {
		t.Errorf("answer = %q", got)
	}
	endAt, textAt := -1, -1
	for i, e := range evs {
		if e.Type == EventThinkingEnd && endAt < 0 {
			endAt = i
		}
		if e.Type == EventTextDelta && textAt < 0 {
			textAt = i
		}
	}
	if endAt < 0 || textAt < 0 || endAt > textAt {
		t.Errorf("the reasoning's end at %d is not before the answer at %d: %v", endAt, textAt, kinds(evs))
	}
}

// A reply that stops inside its reasoning, or goes straight to a tool
// call from it, still closes the reasoning, with all of it said.
func TestInlineReasoningIsClosedHoweverTheReplyEnds(t *testing.T) {
	stopped := contentChunks("<think>ran out of room <") +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n"
	evs := streamOf(t, stopped)
	if thinkingText(evs) != "ran out of room <" || textOf(evs) != "" {
		t.Errorf("stopped: reasoning %q answer %q", thinkingText(evs), textOf(evs))
	}
	ended := 0
	for _, e := range evs {
		if e.Type == EventThinkingEnd {
			ended++
		}
	}
	if ended != 1 {
		t.Errorf("stopped: %d reasoning ends, want 1: %v", ended, kinds(evs))
	}

	tool := contentChunks("<think>need the file") +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{}\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
	evs = streamOf(t, tool)
	if thinkingText(evs) != "need the file" || textOf(evs) != "" {
		t.Errorf("tool: reasoning %q answer %q", thinkingText(evs), textOf(evs))
	}
	endAt, callAt := -1, -1
	for i, e := range evs {
		if e.Type == EventThinkingEnd && endAt < 0 {
			endAt = i
		}
		if e.Type == EventToolUseStart && callAt < 0 {
			callAt = i
		}
	}
	if endAt < 0 || callAt < 0 || endAt > callAt {
		t.Errorf("tool: the reasoning's end at %d is not before the call at %d: %v", endAt, callAt, kinds(evs))
	}
}

// The raw reply /llm-doctor judges is split the same way, and says so;
// a reply with the reasoning in its own field is left as it came.
func TestTheDoctorsReplySplitsInlineReasoning(t *testing.T) {
	for _, tc := range []struct {
		message            string
		content, reasoning string
		inline             bool
	}{
		{`{"content":"<think>thinking it over</think>\n\n391"}`, "391", "thinking it over", true},
		{`{"content":"391","reasoning_content":"thinking it over"}`, "391", "thinking it over", false},
		{`{"content":"391"}`, "391", "", false},
		// An empty block is still tags in front of the answer.
		{`{"content":"<think>\n\n</think>\n\nOK"}`, "OK", "", true},
		// The field's reasoning is kept, and the block is a copy of it.
		{`{"content":"<think>copy</think>OK","reasoning_content":"R"}`, "OK", "R", true},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"choices":[{"message":%s,"finish_reason":"stop"}]}`, tc.message)
		}))
		reply, err := NewOpenAICompat(srv.URL, "").RawChat(context.Background(), []byte(`{"model":"m"}`))
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		if reply.Content != tc.content || reply.Reasoning != tc.reasoning || reply.ReasoningInline != tc.inline {
			t.Errorf("%s: content %q reasoning %q inline %v", tc.message, reply.Content, reply.Reasoning, reply.ReasoningInline)
		}
	}
}

// A bare opening tag followed by a tool call is closed as reasoning by
// the call, never drawn as text beside it.
func TestABareThinkTagBeforeAToolCallIsNotText(t *testing.T) {
	body := contentChunks("<think>") +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{}\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
	evs := streamOf(t, body)
	if got := textOf(evs); got != "" {
		t.Errorf("the tag was drawn as text: %q (%v)", got, kinds(evs))
	}
	body = contentChunks("<think>") + "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n"
	if got := textOf(streamOf(t, body)); got != "" {
		t.Errorf("a reply cut after the tag drew it as text: %q", got)
	}
}

// A server that sends the reasoning in its own field and leaves the block
// in the answer as well (llama.cpp's deepseek-legacy format) shows it
// once: the field's, with the answer free of the copy.
func TestReasoningSentTwiceIsShownOnce(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"R\"}}]}\n\n" +
		contentChunks("<think>R</think>", "OK") +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	evs := streamOf(t, body)
	if thinkingText(evs) != "R" || textOf(evs) != "OK" {
		t.Errorf("reasoning %q answer %q, want R and OK", thinkingText(evs), textOf(evs))
	}
}
