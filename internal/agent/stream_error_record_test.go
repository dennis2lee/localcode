package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"localcode/internal/events"
)

// A reply the model really produced belongs in the record even when the
// turn it belonged to failed.
//
// The defect: on a provider stream error the loop returned from inside
// the read loop, so the message.part.end that closes a reply was never
// appended. The text reached the log as deltas and nothing else — and an
// unterminated reply is one the replay filter later deletes, because
// collapseFinishedDeltas drops every delta lying before the last part.end
// in the range. So the half-written answer was on screen while it
// happened, and then vanished from every later attach as soon as any
// other message completed. The bytes stayed in the log; no client ever
// read them again.
//
// This is the record, which is a separate question from the history the
// model is sent — that still excludes the failed turn, and should.
func TestAReplyCutOffByAStreamErrorStaysInTheRecord(t *testing.T) {
	const said = "the answer so far"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"content":"`+said+`"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		// The upstream drops mid-reply. Hijacking and closing is the
		// honest reproduction: the client sees a torn body rather than a
		// finished stream, which is what makes the provider report an
		// error instead of a clean stop.
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("test server cannot hijack, so the tear cannot be reproduced")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.Close()
	}))
	defer server.Close()

	loop, sessionID := testLoop(t, server.URL)
	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "a question"); err == nil {
		t.Fatal("expected the torn stream to fail the turn")
	}

	log, err := loop.Store.Events(sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ended, errored bool
	for _, ev := range log {
		switch ev.Type {
		case events.TypeMessagePartEnd:
			if text, _ := ev.Data["text"].(string); strings.Contains(text, said) {
				ended = true
			}
		case events.TypeError:
			errored = true
		}
	}
	if !ended {
		t.Errorf("the log has no message.part.end holding %q, so the replay filter will delete what the model said:\n%s", said, dumpLog(log))
	}
	if !errored {
		t.Errorf("the failure itself is not in the log:\n%s", dumpLog(log))
	}
}

// The other half: a stream that dies before the model says anything must
// not invent an empty reply. There is nothing to keep, and a blank
// message in the transcript is a claim that the model answered.
func TestAStreamThatDiesSilentlyRecordsNoReply(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("test server cannot hijack")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.Close()
	}))
	defer server.Close()

	loop, sessionID := testLoop(t, server.URL)
	_ = loop.SendMessage(context.Background(), sessionID, "general-purpose", "a question")

	log, err := loop.Store.Events(sessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range log {
		if ev.Type == events.TypeMessagePartEnd {
			t.Errorf("a reply was recorded for a stream that produced nothing:\n%s", dumpLog(log))
		}
	}
}

func dumpLog(log []events.Event) string {
	var b strings.Builder
	for _, ev := range log {
		fmt.Fprintf(&b, "  %d %s %v\n", ev.Seq, ev.Type, ev.Data["text"])
	}
	return b.String()
}
