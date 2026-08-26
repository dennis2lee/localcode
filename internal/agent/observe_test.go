package agent

import (
	"context"
	"strings"
	"testing"

	"localcode/internal/trace"
)

func withTracing(t *testing.T, loop *Loop) *trace.Writer {
	t.Helper()
	w, err := trace.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open trace: %v", err)
	}
	t.Cleanup(func() { w.Close() })
	loop.Trace = w
	return w
}

func spans(records []trace.Record) []string {
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, r.Span)
	}
	return out
}

// One turn, written down: what it started as, what answered, and what it
// cost. Without this a session that is slow or expensive has nothing to
// look at but its own transcript, which says none of it.
func TestATurnIsRecorded(t *testing.T) {
	srv, _ := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)
	w := withTracing(t, loop)

	sendOne(t, loop, "s1", "general-purpose")

	got := spans(w.Recent(50, "s1", ""))
	if strings.Join(got, ",") != "turn.start,model,turn.end" {
		t.Fatalf("spans were %v, want the turn, the model call, and the end", got)
	}
	records := w.Recent(50, "s1", "")
	if records[1].Model != "claude-opus-5" || records[1].Provider != "local" {
		t.Errorf("the model call recorded %q on %q", records[1].Model, records[1].Provider)
	}
	if records[1].DurationMS < 0 {
		t.Error("no duration on the model call")
	}
	// One id for the whole turn is what makes the file a story rather
	// than a pile of lines.
	if records[0].TraceID == "" || records[0].TraceID != records[2].TraceID {
		t.Errorf("trace ids were %q and %q", records[0].TraceID, records[2].TraceID)
	}
}

// Off is off. The log names which models answered and what they cost,
// under the user's home directory, which is a thing to opt into.
func TestNothingIsRecordedWithSmartAgentOff(t *testing.T) {
	srv, _ := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)
	w := withTracing(t, loop)

	sendOne(t, loop, "s1", "general-purpose")

	if got := w.Recent(50, "", ""); len(got) != 0 {
		t.Errorf("wrote %d records with the feature off: %v", len(got), spans(got))
	}
}

// The multi-agent correlation, which is the whole reason a trace id
// exists: a delegation and the sub-agent's own model call have to come
// back under one id, from two different sessions.
func TestASubAgentRecordsUnderTheSameTrace(t *testing.T) {
	srv := backgroundServer(t)
	defer srv.Close()
	loop, _ := newBackgroundLoop(t, srv.URL)
	w := withTracing(t, loop)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	ctx := trace.WithID(WithSessionID(context.Background(), sid), "fixed-trace")

	if res := launch(t, loop.Tools, ctx, "explore", "say hello"); res.IsError {
		t.Fatalf("launch: %s", res.Content)
	}
	if res := loop.Tools.Call(ctx, "TaskCollect", []byte(`{}`), ""); res.IsError {
		t.Fatalf("collect: %s", res.Content)
	}

	records := w.Recent(50, "", "fixed-trace")
	var sawDelegate, sawChildModel bool
	for _, r := range records {
		if r.Span == trace.SpanDelegate && r.SessionID == sid {
			sawDelegate = true
		}
		if r.Span == trace.SpanModel && r.ParentSessionID == sid {
			sawChildModel = true
		}
	}
	if !sawDelegate {
		t.Errorf("no delegation was recorded: %v", spans(records))
	}
	if !sawChildModel {
		t.Errorf("the sub-agent's own work was not recorded under the parent's trace: %v", spans(records))
	}
}

// A fallback is the fact a reader most needs and the transcript is worst
// at carrying: the answer arrived, and it came from somewhere else.
func TestAFallbackIsRecordedWithTheTurn(t *testing.T) {
	srv, _ := failingServer(t, 429, "rate limit exceeded", 1)
	defer srv.Close()
	loop := newFallbackLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)
	w := withTracing(t, loop)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	records := w.Recent(50, sid, "")
	var fallback, end *trace.Record
	for i := range records {
		switch records[i].Span {
		case trace.SpanFallback:
			fallback = &records[i]
		case trace.SpanTurnEnd:
			end = &records[i]
		}
	}
	if fallback == nil {
		t.Fatalf("no fallback recorded: %v", spans(records))
	}
	if fallback.Model != "qwen3-coder-30b" || fallback.Error == "" {
		t.Errorf("fallback record = %+v", *fallback)
	}
	if end == nil || end.Fallbacks != 1 {
		t.Errorf("the turn did not end reporting one fallback: %+v", end)
	}
}
