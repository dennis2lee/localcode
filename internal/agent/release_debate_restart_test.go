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
	"time"

	"localcode/internal/events"
	"localcode/internal/provider"
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
	// Pinned on the log rather than through a live turn: whether a
	// compaction lands inside a debate is the compaction policy's call,
	// and a policy change had already stopped the end-to-end version from
	// compacting at all while it went on passing. This is the log a
	// compaction at the top of round one writes, and the history the
	// live session holds after it: the summary, the task and the answer.
	task := "write a sum function"
	msg := func(role provider.Role, text string) provider.Message {
		return provider.Message{Role: role, Content: []provider.Block{provider.TextBlock(text)}}
	}
	evs := []events.Event{
		ev(events.TypeUserMessage, map[string]any{"text": strings.Repeat("a long first prompt ", 200)}),
		ev(events.TypeDebateStarted, map[string]any{"task": task}),
		ev(events.TypeCompacted, map[string]any{"summary": "the conversation so far", "summary_length": 23}),
		ev(events.TypeUserMessage, map[string]any{"text": task}),
		ev(events.TypeToolStart, map[string]any{"tool_use_id": "t1", "name": "glob"}),
		ev(events.TypeMessagePartEnd, map[string]any{"text": ""}),
		ev(events.TypeToolEnd, map[string]any{"tool_use_id": "t1", "input": "{}", "content": "main.go"}),
		ev(events.TypeMessagePartEnd, map[string]any{"text": "the sum function"}),
		ev(events.TypeDebateEnded, map[string]any{"reason": "approved", "collapsed": true}),
	}
	rebuilt := rehydrateHistory(evs)
	live := collapsedDebate([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Block{{Type: provider.BlockText, Text: summaryHeader + "the conversation so far", Source: compactSummarySource}}},
		msg(provider.RoleUser, task),
		{Role: provider.RoleAssistant, Content: []provider.Block{{Type: provider.BlockToolUse, ToolUseID: "t1", ToolName: "glob"}}},
		{Role: provider.RoleUser, Content: []provider.Block{provider.ToolResultBlock("t1", "main.go", false)}},
		msg(provider.RoleAssistant, "the sum function"),
	}, 1, task)
	if got, want := historyShape(rebuilt), historyShape(live); got != want {
		t.Errorf("a restart rebuilt a debate compacted at its top as:\n%s\nthe live session holds:\n%s", got, want)
	}
	if len(rebuilt) != 3 {
		t.Errorf("rebuilt %d messages, want the summary, the task and the answer:\n%s", len(rebuilt), historyShape(rebuilt))
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

// Stop pressed while one reviewer is still reading does not hide the
// other reviewer's own failure: it failed before Stop, for its own reason,
// and the record says so. The one Stop cancelled says nothing.
func TestStopDoesNotHideAReviewersOwnFailure(t *testing.T) {
	arrived := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := dbgRead(r)
		switch req.model {
		case thirdModel:
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"this reviewer's model is not available"}}`)
		case reviewModel:
			select {
			case arrived <- struct{}{}:
			default:
			}
			<-r.Context().Done()
		default:
			dbgSend(w, textChunks("first attempt")...)
		}
	}))
	defer srv.Close()
	loop := newDebateLoop(t, srv.URL)
	sid := startDebateSession(t, loop)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Stop once the other reviewer's turn has failed on its own: waited
	// for on the record, not on a clock.
	tomFailed := func() bool {
		for _, s := range loop.Store.AllSessions() {
			if s.Agent != "tom" {
				continue
			}
			evs, _ := loop.Store.Events(s.ID, 0)
			for _, e := range evs {
				if e.Type == events.TypeError {
					if r, _ := e.Data["recovered"].(bool); !r {
						return true
					}
				}
			}
		}
		return false
	}
	go func() {
		<-arrived
		deadline := time.Now().Add(10 * time.Second)
		for !tomFailed() && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	_ = loop.SendMessage(ctx, sid, "boy", "/debate girl,tom 2 write a sum function")
	var errs []string
	for _, e := range debateEvents(t, loop, sid, events.TypeError) {
		errs = append(errs, dataString(e.Data, "error"))
	}
	var tom, girl bool
	for _, e := range errs {
		if strings.Contains(e, "the tom agent could not review") {
			tom = true
		}
		if strings.Contains(e, "the girl agent could not review") {
			girl = true
		}
	}
	if !tom {
		t.Errorf("the reviewer that failed on its own before Stop left no record: %q", errs)
	}
	if girl {
		t.Errorf("the reviewer Stop cancelled is reported as one that could not review: %q", errs)
	}
}

// The author's own turn failing ends the debate as failed, with a note
// that says it was the author's turn, not that no review came back.
func TestAnAuthorsFailedTurnSaysWhoseItWas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"this model is not available"}}`)
	}))
	defer srv.Close()
	loop := newDebateLoop(t, srv.URL)
	sid := startDebateSession(t, loop)
	_ = loop.SendMessage(context.Background(), sid, "boy", "/debate girl 2 write a sum function")
	ended := debateEvents(t, loop, sid, events.TypeDebateEnded)
	if len(ended) != 1 {
		t.Fatalf("%d debate.ended events", len(ended))
	}
	if reason := dataString(ended[0].Data, "reason"); reason != "failed" {
		t.Errorf("reason = %q, want failed", reason)
	}
	note := dataString(ended[0].Data, "note")
	if !strings.Contains(note, "boy's turn failed") || strings.Contains(note, "no review came back") {
		t.Errorf("note = %q, want it to say the author's turn failed", note)
	}
}
