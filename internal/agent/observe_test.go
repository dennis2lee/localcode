package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	quickRetries(t)
	srv, _ := failingServer(t, 429, "rate limit exceeded", 3)
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
	var fallback, retry, end *trace.Record
	for i := range records {
		switch records[i].Span {
		case trace.SpanFallback:
			fallback = &records[i]
		case trace.SpanRetry:
			retry = &records[i]
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
	// The retries that preceded the switch are part of the same story: a
	// turn that paused twice and then moved is not a turn that moved at
	// the first refusal.
	if retry == nil {
		t.Fatalf("no retry recorded: %v", spans(records))
	}
	if retry.Model != "claude-opus-5" || retry.Error == "" {
		t.Errorf("retry record = %+v", *retry)
	}
	if end == nil || end.Fallbacks != 1 || end.Retries != 2 {
		t.Errorf("the turn did not end reporting one fallback and two retries: %+v", end)
	}
}

// SA3, the turn half. The switch used to be read again at every provider
// round, every tool call and every trace record, so flipping it during a
// tool loop split one turn between two sets of rules: a trace with a start
// and no end, cache markers that stopped mid-turn, credential guards that
// appeared or vanished under a turn already running.
//
// The turn is admitted under one answer and keeps it. Flipping still takes
// effect, on the next turn.
func TestATurnKeepsTheSmartAgentStateItStartedUnder(t *testing.T) {
	var loop *Loop
	var mu sync.Mutex
	round := 0

	var enums [][]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The delegation enum this round actually advertised. Read off the
		// wire rather than from a helper, because "what the model was
		// told" is the thing that has to stay put.
		var body struct {
			Tools []struct {
				Function struct {
					Name       string `json:"name"`
					Parameters struct {
						Properties struct {
							Agent struct {
								Enum []string `json:"enum"`
							} `json:"agent"`
						} `json:"properties"`
					} `json:"parameters"`
				} `json:"function"`
			} `json:"tools"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		for _, tl := range body.Tools {
			if tl.Function.Name == "Task" {
				enums = append(enums, tl.Function.Parameters.Properties.Agent.Enum)
			}
		}
		n := round
		round++
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		if n == 0 {
			// The first round asks for a tool, which is what puts the
			// turn into the loop where the switch used to be re-read.
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"glob","arguments":"{\"pattern\":\"*.go\"}"}}]}}]}`+"\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			w.(http.Flusher).Flush()
			// Off, while the turn is still running.
			loop.SetSmartAgentEnabled(false)
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	loop = newSmartLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)
	tw := withTracing(t, loop)

	sendOne(t, loop, "s1", "general-purpose")

	got := spans(tw.Recent(50, "s1", ""))
	joined := strings.Join(got, ",")
	if !strings.HasPrefix(joined, "turn.start") {
		t.Fatalf("spans were %v, want the turn recorded at all", got)
	}
	if !strings.HasSuffix(joined, "turn.end") {
		t.Fatalf("spans were %v, want a turn.end — the switch went off mid-loop and the turn stopped being written down", got)
	}
	if !strings.Contains(joined, "tool") {
		t.Errorf("spans were %v, want the tool call recorded under the same turn", got)
	}
	// SA3, round 2. The tool schema is half of the stable prefix this turn
	// is marking as cacheable, and it is what the model is told it may
	// delegate to. Both rounds have to advertise the same roster, and the
	// runtime accepted that roster: a turn that advertises one capability
	// and executes another is worse than one that does neither.
	mu.Lock()
	advertised := append([][]string(nil), enums...)
	mu.Unlock()
	if len(advertised) != 2 {
		t.Fatalf("captured %d Task schemas, want one per provider round", len(advertised))
	}
	if len(advertised[0]) == 0 {
		t.Fatal("the first round advertised no delegation roster at all")
	}
	if strings.Join(advertised[0], ",") != strings.Join(advertised[1], ",") {
		t.Errorf("the delegation enum changed mid-turn: %v then %v", advertised[0], advertised[1])
	}

	// The next turn is the one that sees the change.
	sendOne(t, loop, "s2", "general-purpose")
	if after := tw.Recent(200, "s2", ""); len(after) != 0 {
		t.Errorf("a turn started after Smart Agent went off wrote %d records, want none", len(after))
	}
}

// OB-01: the compaction call is a model call and carries a manifest like
// any other, on its own span — a utility call nobody could ask about
// would be the one exception to "every call names its assembly".
func TestTheCompactionCallCarriesAManifest(t *testing.T) {
	srv, _ := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)
	w := withTracing(t, loop)

	const sid = "s1"
	if _, err := loop.Store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sendOne(t, loop, sid, "general-purpose")
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/compact"); err != nil {
		t.Fatalf("/compact: %v", err)
	}

	var found bool
	for _, rec := range w.Recent(100, sid, "") {
		if rec.Span == trace.SpanCompact && rec.PromptManifest != "" {
			found = true
			var hasUtility bool
			for _, id := range rec.PromptAssets {
				if id == AssetCompactPrompt {
					hasUtility = true
				}
			}
			if !hasUtility {
				t.Errorf("the compaction span's assets %v do not include its own instruction", rec.PromptAssets)
			}
		}
	}
	if !found {
		t.Error("no compaction span carried a manifest id")
	}
}
