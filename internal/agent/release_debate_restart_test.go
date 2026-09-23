package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"localcode/internal/events"
)

// A debate's end, a compaction inside one, Stop pressed during its
// reviews, and a trim that replaces the history to fit the window: each
// has to leave the live session, a restarted one and both clients' gauges
// agreeing about what the conversation holds.

func dbgSend(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	w.(http.Flusher).Flush()
}
func dbgUsage(prompt, completion int) string {
	return fmt.Sprintf(`{"choices":[],"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`, prompt, completion)
}
func dbgOverflow(w http.ResponseWriter) {
	w.WriteHeader(http.StatusBadRequest)
	fmt.Fprint(w, `{"error":{"message":"This model's maximum context length is 8192 tokens. However, your prompt contains more."}}`)
}

type dbgReq struct{ model, raw, lastRole string }

func dbgRead(r *http.Request) dbgReq {
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Model    string           `json:"model"`
		Messages []map[string]any `json:"messages"`
	}
	_ = json.Unmarshal(raw, &body)
	last := ""
	for _, m := range body.Messages {
		if role, _ := m["role"].(string); role != "system" {
			last = role
		}
	}
	return dbgReq{model: body.Model, raw: string(raw), lastRole: last}
}

func TestARestartDropsTheCountOfADebateItCollapsesAgain(t *testing.T) {
	var mu sync.Mutex
	authorCalls, reviewRounds := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := dbgRead(r)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case req.model == authorModel && strings.Contains(req.raw, "Summarize our conversation so far"):
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"summarization is not available on this server"}}`)
		case req.model == authorModel:
			authorCalls++
			switch authorCalls {
			case 1:
				dbgSend(w, append(textChunks("hi there"), dbgUsage(1000, 10))...)
			case 2:
				dbgSend(w, append(textChunks("first attempt"), dbgUsage(2000, 10))...)
			case 3:
				dbgOverflow(w)
			default:
				dbgSend(w, append(textChunks("second attempt"), dbgUsage(3000, 10))...)
			}
		default:
			if req.lastRole == "tool" {
				dbgSend(w, textChunks("review done")...)
				return
			}
			reviewRounds++
			approve := reviewRounds >= 2
			dbgSend(w, toolCallChunks("call_verdict", verdictToolName,
				fmt.Sprintf(`{\"approved\":%t,\"findings\":\"name the variables better\"}`, approve))...)
		}
	}))
	defer srv.Close()

	loop := newDebateLoop(t, srv.URL)
	sid := startDebateSession(t, loop)
	if err := loop.SendMessage(context.Background(), sid, "boy", "hello"); err != nil {
		t.Fatalf("plain turn: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "boy", "/debate girl 2 write a sum function"); err != nil {
		t.Fatalf("debate: %v", err)
	}
	ended := debateEvents(t, loop, sid, events.TypeDebateEnded)
	if len(ended) != 1 {
		t.Fatalf("precondition: %d debate.ended events", len(ended))
	}
	if c, said := ended[0].Data["collapsed"].(bool); !said || c {
		t.Fatalf("precondition: forceFit should have made the mark stale; debate.ended = %v", ended[0].Data)
	}
	trimmed := false
	all, _ := loop.Store.Events(sid, 0)
	for _, ev := range all {
		if ev.Type == events.TypeError && strings.Contains(dataString(ev.Data, "error"), "trimming instead") {
			trimmed = true
		}
	}
	if !trimmed {
		t.Fatalf("precondition: forceFit never ran")
	}
	live := joinMessages(loop.history(sid))
	liveUsage, liveHave := loop.getUsage(sid)
	if !strings.Contains(live, "Fix what you agree with") {
		t.Fatalf("precondition: the live history does not hold the round brief:\n%s", live)
	}
	loop.setHistory(sid, nil)
	loop.RehydrateSession(sid)
	restored := joinMessages(loop.history(sid))
	restoredUsage, restoredHave := loop.getUsage(sid)
	t.Logf("live history:\n%s\nlive count: have=%v %+v", live, liveHave, liveUsage)
	t.Logf("restored history:\n%s\nrestored count: have=%v %+v", restored, restoredHave, restoredUsage)
	// The restart decides the collapse from the history it rebuilds, which
	// does not hold the live trim, so it may collapse rounds the live
	// session kept. Whatever it decides, a count taken over the rounds
	// must not survive into it.
	if restoredHave {
		t.Errorf("a count taken over the debate rounds survived a restart that rebuilt the history without the live trim: input=%d measured=%d",
			restoredUsage.InputTokens, restoredUsage.Measured)
	}
}

func TestACompactionInsideADebateDoesNotUndoItsCollapse(t *testing.T) {
	summary := strings.Repeat("summary ", 1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := dbgRead(r)
		switch {
		case strings.Contains(req.raw, "Summarize our conversation so far"):
			dbgSend(w, textChunks(summary)...)
		case req.model == authorModel && req.lastRole == "tool":
			dbgSend(w, textChunks("the sum function")...)
		case req.model == authorModel && strings.Contains(req.raw, "write a sum function"):
			dbgSend(w, toolCallChunks("call_glob", "glob", `{\"pattern\":\"*.go\"}`)...)
		case req.model == authorModel:
			dbgSend(w, textChunks("hi")...)
		case req.lastRole == "tool":
			dbgSend(w, textChunks("review done")...)
		default:
			dbgSend(w, toolCallChunks("call_verdict", verdictToolName,
				`{\"approved\":true,\"findings\":\"fine\"}`)...)
		}
	}))
	defer srv.Close()

	loop := newDebateLoop(t, srv.URL)
	loop.SetAutoCompactEnabled(true)
	loop.SetCompactPercent(1) // stands in for a small window where system prompt + summary cross the default 50%
	sid := startDebateSession(t, loop)
	for _, line := range []string{"hello", "/compact", "/debate girl 1 write a sum function"} {
		if err := loop.SendMessage(context.Background(), sid, "boy", line); err != nil {
			t.Fatalf("%s: %v", line, err)
		}
	}
	ended := debateEvents(t, loop, sid, events.TypeDebateEnded)
	if len(ended) != 1 || ended[0].Data["collapsed"] != true {
		t.Fatalf("precondition: want one debate.ended with collapsed:true, got %v", ended)
	}
	short := func(s string) string { return strings.ReplaceAll(s, summary, "<summary>") }
	liveN := len(loop.history(sid))
	live := joinMessages(loop.history(sid))
	loop.setHistory(sid, nil)
	loop.RehydrateSession(sid)
	restoredN := len(loop.history(sid))
	restored := joinMessages(loop.history(sid))
	if restored != live {
		t.Errorf("debate.ended says collapsed:true but a restart rebuilt the rounds:\n--- live (%d messages) ---\n%s--- restored (%d messages) ---\n%s",
			liveN, short(live), restoredN, short(restored))
	}
}
func TestStopDuringTheReviewsIsAStop(t *testing.T) {
	arrived := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := dbgRead(r)
		if req.model == reviewModel {
			select {
			case arrived <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return
		}
		dbgSend(w, textChunks("first attempt")...)
	}))
	defer srv.Close()
	loop := newDebateLoop(t, srv.URL)
	sid := startDebateSession(t, loop)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-arrived
		cancel()
	}()
	_ = loop.SendMessage(ctx, sid, "boy", "/debate girl 2 write a sum function")
	ended := debateEvents(t, loop, sid, events.TypeDebateEnded)
	if len(ended) != 1 {
		t.Fatalf("%d debate.ended events", len(ended))
	}
	var errs []string
	for _, ev := range debateEvents(t, loop, sid, events.TypeError) {
		errs = append(errs, dataString(ev.Data, "error"))
	}
	if reason := dataString(ended[0].Data, "reason"); reason != "stopped" {
		t.Errorf("Stop pressed during the review ended the debate as %q, note %q; errors logged: %q",
			reason, dataString(ended[0].Data, "note"), errs)
	}
	for _, e := range errs {
		if strings.Contains(e, "could not review") {
			t.Errorf("a reviewer Stop cancelled is reported as one that could not review: %q", e)
		}
	}
}
func forgetsFill(ev events.Event) bool {
	switch ev.Type {
	case events.TypeCleared, events.TypeCompacted, events.TypeRewound, events.TypeRedone:
		return true
	case events.TypeDebateEnded:
		c, said := ev.Data["collapsed"].(bool)
		return !said || c
	case events.TypeError:
		return ev.Data["history_replaced"] == true
	}
	return false
}

func TestATrimThatReplacesTheHistorySaysSo(t *testing.T) {
	var mu sync.Mutex
	authorCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := dbgRead(r)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.Contains(req.raw, "Summarize our conversation so far"):
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"summarization is not available on this server"}}`)
		default:
			authorCalls++
			if authorCalls == 1 {
				dbgSend(w, append(textChunks("hi there"), dbgUsage(20000, 10))...)
				return
			}
			dbgOverflow(w)
		}
	}))
	defer srv.Close()
	loop := newDebateLoop(t, srv.URL)
	sid := startDebateSession(t, loop)
	if err := loop.SendMessage(context.Background(), sid, "boy", "hello "+strings.Repeat("x", 4000)); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	_ = loop.SendMessage(context.Background(), sid, "boy", "again")
	all, _ := loop.Store.Events(sid, 0)
	lastUsage := -1
	trimmed := false
	for i, ev := range all {
		if ev.Type == events.TypeUsage {
			lastUsage = i
		}
		if ev.Type == events.TypeError && strings.Contains(dataString(ev.Data, "error"), "trimming instead") {
			trimmed = true
		}
	}
	if lastUsage < 0 || !trimmed {
		t.Fatalf("precondition: usage at %d, trimmed=%v", lastUsage, trimmed)
	}
	var after []string
	forgot := false
	for _, ev := range all[lastUsage+1:] {
		after = append(after, string(ev.Type))
		if forgetsFill(ev) {
			forgot = true
		}
	}
	_, liveHave := loop.getUsage(sid)
	if !liveHave && !forgot {
		t.Errorf("the daemon replaced the history (forceFit) and dropped its count, but nothing after the last "+
			"usage event (percent %v) tells a client to let go of it; events since: %v; history now: %q",
			all[lastUsage].Data["percent"], after, joinMessages(loop.history(sid)))
	}
}
