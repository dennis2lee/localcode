package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"localcode/internal/events"
)

// A unit cut off part way through its answer, by Esc, the stage timeout
// or the run deadline, did not finish: a resume runs it again rather than
// reusing the half it had written.
func TestAUnitCutOffMidAnswerIsNotReusedByAResume(t *testing.T) {
	var cutOff atomic.Bool
	cutOff.Store(true)
	var started, askedB atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(string(raw), "look at b") {
			askedB.Add(1)
			if cutOff.Load() {
				started.Add(1)
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"half an ans\"}}]}\n\n")
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					return
				case <-time.After(3 * time.Second):
				}
			}
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"the whole answer\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	loop := orchestrateLoop(t, srv.URL)

	// Esc while b is half way through its answer.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for started.Load() == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	res := executeOrchestrate(ctx, loop, `{"goal":"g","stages":[
	  {"name":"look","kind":"fanout","agent":"oracle","over":["a","b"],"prompt":"look at {{item}}"}]}`)
	if !strings.Contains(res.Content, "context canceled") {
		t.Fatalf("the run was not cancelled:\n%s", res.Content)
	}
	run := runIDs(loop, "main")[0]
	evs, _ := loop.Store.Events(run, 0)
	for _, e := range evs {
		if e.Type == events.TypeTaskStatus && e.Data["reply"] == "half an ans" {
			t.Errorf("the cancelled unit is in the journal as %v", e.Data)
		}
	}

	cutOff.Store(false)
	before := askedB.Load()
	out := resume(t, loop, "main", run)
	if askedB.Load() == before {
		t.Errorf("the resume did not run the cancelled unit again; it reused the half answer:\n%s", out)
	}
}

// The same cut-off unit, in a stage that says unanswered: fail, stops the
// run as cancelled, not as an agent that would not answer.
func TestEscMidAnswerIsNotReportedAsAnUnansweredAgent(t *testing.T) {
	var started atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		started.Add(1)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"thinking about it\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(3 * time.Second):
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	t.Cleanup(srv.Close)
	loop := orchestrateLoop(t, srv.URL)
	ctx, cancel := context.WithCancel(loop.pinSmart(context.Background()))
	go func() {
		for started.Load() == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	p := planOf(t, `{"goal":"g","stages":[
	  {"name":"look","kind":"step","agent":"oracle","prompt":"look","returns":{"seen":"string"},"unanswered":"fail"},
	  {"name":"sum","kind":"step","agent":"oracle","prompt":"sum {{input}}"}]}`)
	r := loop.runPlan(ctx, "main", "main", p)
	if !strings.Contains(r.stopped, "cancelled") {
		t.Errorf("Esc during the answer was reported as %q", r.stopped)
	}
}
