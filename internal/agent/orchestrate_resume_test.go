package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"localcode/internal/events"
)

// Resuming a run reuses what finished and runs what did not.
//
// A run that stopped half way, on a provider error, a timeout, or a person
// pressing Esc, has paid for every unit that completed. These pin that a
// resume pays for the rest and nothing else, and refuses where running
// the same units twice, or running another conversation's, is the risk.

// failingModel is m behind a proxy that answers 500 to every request whose
// body contains one of the markers in fail, so a test can fail chosen
// units and then stop failing them.
func failingModel(t *testing.T, m *scriptedModel, fail *sync.Map) string {
	t.Helper()
	upstream := m.server(t).URL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		failed := false
		fail.Range(func(k, _ any) bool {
			failed = bytes.Contains(body, []byte(k.(string)))
			return !failed
		})
		if failed {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		resp, err := http.Post(upstream+r.URL.Path, r.Header.Get("Content-Type"), bytes.NewReader(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		io.Copy(w, resp.Body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// lookThenSum fans out over four items that each answer, then sums them.
const lookThenSum = `{"goal":"look at four things","stages":[
  {"name":"look","kind":"fanout","agent":"oracle","over":["a","b","c","d"],"prompt":"look at {{item}}","returns":{"seen":"string"}},
  {"name":"sum","kind":"step","agent":"oracle","prompt":"sum up {{input}}"}]}`

// resume types "/workflow resume [id]" into main and returns the reply.
func resume(t *testing.T, loop *Loop, session, id string) string {
	t.Helper()
	line := strings.TrimSpace("/workflow resume " + id)
	if err := loop.SendMessage(context.Background(), session, "oracle", line); err != nil {
		t.Fatalf("%s: %v", line, err)
	}
	return lastAssistantText(loop.Store, session)
}

func runIDs(loop *Loop, session string) []string {
	var ids []string
	for _, s := range loop.Store.Children(session) {
		if s.Agent == runSessionAgent {
			ids = append(ids, s.ID)
		}
	}
	return ids
}

// What failed and what never answered run again; what answered is taken
// from the journal with no model turn; and a resume of the resume finds
// everything, including the units it reused itself.
func TestAResumeRunsOnlyWhatDidNotFinish(t *testing.T) {
	var noAnswer atomic.Bool
	noAnswer.Store(true)
	m := &scriptedModel{reply: func(p string) (string, map[string]any) {
		if strings.Contains(p, "sum up") {
			return "summed", nil
		}
		if i := strings.LastIndex(p, "look at "); i >= 0 {
			item := p[i+len("look at "):]
			if item == "c" && noAnswer.Load() {
				return "I looked but will not say", nil
			}
			return "looked at " + item, map[string]any{"seen": item}
		}
		return "summed", nil
	}}
	fail := &sync.Map{}
	fail.Store("look at d", true)
	loop := orchestrateLoop(t, failingModel(t, m, fail))

	executeOrchestrate(context.Background(), loop, lookThenSum)
	first := runIDs(loop, "main")
	if len(first) != 1 {
		t.Fatalf("runs = %v, want one", first)
	}

	fail.Delete("look at d")
	noAnswer.Store(false)
	before := len(m.seen())
	out := resume(t, loop, "main", "")
	asked := m.seen()[before:]
	for _, p := range asked {
		for _, done := range []string{"look at a", "look at b"} {
			if strings.HasSuffix(p, done) {
				t.Errorf("a unit that answered was asked again: %q", p)
			}
		}
	}
	var reran []string
	for _, p := range asked {
		for _, item := range []string{"look at c", "look at d", "sum up"} {
			if strings.HasSuffix(p, item) || item == "sum up" && strings.Contains(p, item) {
				reran = append(reran, item)
			}
		}
	}
	for _, want := range []string{"look at c", "look at d", "sum up"} {
		if !strings.Contains(strings.Join(reran, "|"), want) {
			t.Errorf("%q did not run again; the resume asked %q", want, asked)
		}
	}
	if !strings.Contains(out, "Resumed run "+first[0]) || !strings.Contains(out, "5 agent turns") ||
		!strings.Contains(out, "2 of them answers reused") {
		t.Errorf("the reply does not report the resume:\n%s", out)
	}
	if !strings.Contains(out, "(reused)") {
		t.Errorf("the report does not mark the reused results:\n%s", out)
	}

	before = len(m.seen())
	out = resume(t, loop, "main", "")
	if n := len(m.seen()) - before; n != 0 {
		t.Errorf("resuming a run that finished asked a model %d times: %q", n, m.seen()[before:])
	}
	if !strings.Contains(out, "5 of them answers reused") {
		t.Errorf("a resume of the resume did not find the units the resume reused:\n%s", out)
	}
	if ids := runIDs(loop, "main"); len(ids) != 3 {
		t.Errorf("runs = %v, want the run and two resumes", ids)
	}
}

// A resume names the run it resumes in its record, and is found by id.
func TestAResumeIsFoundByIDAndRecordsWhatItResumed(t *testing.T) {
	m := &scriptedModel{reply: func(p string) (string, map[string]any) { return "ok", nil }}
	loop := orchestrateLoop(t, m.server(t).URL)
	executeOrchestrate(context.Background(), loop, `{"goal":"one","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"first"}]}`)
	executeOrchestrate(context.Background(), loop, `{"goal":"two","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"second"}]}`)
	evs, _ := loop.Store.Events("main", 0)
	var first string
	for _, e := range evs {
		if e.Type == events.TypeTaskSpawned && e.Data["prompt"] == "one" {
			first, _ = e.Data["task_id"].(string)
		}
	}

	before := len(m.seen())
	resume(t, loop, "main", first)
	if n := len(m.seen()) - before; n != 0 {
		t.Errorf("resuming a finished run by id asked a model %d times", n)
	}
	evs, _ = loop.Store.Events("main", 0)
	var last map[string]any
	for _, e := range evs {
		if e.Type == events.TypeTaskSpawned && e.Data["agent"] == runSessionAgent {
			last = e.Data
		}
	}
	if first == "" || last["resumed_from"] != first || last["prompt"] != "one" {
		t.Errorf("the resume's record is %v, want resumed_from %s and the first run's goal", last, first)
	}
}

// Four refusals: nothing to resume, a run still going, a run another
// conversation owns, and a run already being resumed.
func TestAResumeIsRefusedWhereItWouldRunSomethingTwice(t *testing.T) {
	var started atomic.Int32
	release := make(chan struct{})
	m := &scriptedModel{reply: func(p string) (string, map[string]any) {
		if strings.Contains(p, "slowly") {
			started.Add(1)
			// Bounded, so a resume that wrongly starts the live run
			// again fails the test instead of hanging it.
			select {
			case <-release:
			case <-time.After(5 * time.Second):
			}
		}
		return "ok", nil
	}}
	loop := orchestrateLoop(t, m.server(t).URL)

	if _, err := loop.Store.CreateSession("empty", "", "oracle", true); err != nil {
		t.Fatal(err)
	}
	if out := resume(t, loop, "empty", ""); !strings.Contains(out, "Not resumed: no orchestration has run") {
		t.Errorf("a conversation with no run:\n%s", out)
	}
	if out := resume(t, loop, "main", "task-nope"); !strings.Contains(out, "there is no run task-nope") {
		t.Errorf("an unknown run:\n%s", out)
	}

	done := make(chan struct{})
	go func() {
		executeOrchestrate(context.Background(), loop, `{"goal":"g","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"slowly"}]}`)
		close(done)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for started.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	run := runIDs(loop, "main")[0]
	if out := resume(t, loop, "main", ""); !strings.Contains(out, "is still running") {
		t.Errorf("a run in flight:\n%s", out)
	}
	close(release)
	<-done

	evs, _ := loop.Store.Events("main", 0)
	if _, err := loop.Store.CreateSession("fork", "", "oracle", true); err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Type == events.TypeTaskSpawned {
			loop.Store.Append("fork", e.Type, e.Data)
		}
	}
	if out := resume(t, loop, "fork", ""); !strings.Contains(out, "belongs to another conversation") {
		t.Errorf("a fork's copy of the run:\n%s", out)
	}

	loop.mu.Lock()
	loop.resuming = map[string]bool{run: true}
	loop.mu.Unlock()
	if out := resume(t, loop, "main", ""); !strings.Contains(out, "being resumed already") {
		t.Errorf("a second resume of the same run:\n%s", out)
	}
	if ids := runIDs(loop, "main"); len(ids) != 1 {
		t.Errorf("a refused resume started a run: %v", ids)
	}
	if kids := loop.Store.Children("fork"); len(kids) != 0 {
		t.Error("the fork's refused resume started a run")
	}
}

// A run whose stages can write asks before it is resumed, and the
// question says so; a refusal runs nothing.
func TestResumingARunThatWritesAsks(t *testing.T) {
	loop, _, _, asks := workflowLoop(t, func(string) (string, map[string]any) { return "ok", nil })
	plan := `{"goal":"fix it","stages":[{"name":"fix","kind":"step","agent":"oracle","role":"builder","prompt":"fix"}]}`
	if res := NewOrchestrateTool(loop).Execute(WithSessionID(loop.pinSmart(context.Background()), "conv"), json.RawMessage(plan)); res.IsError {
		t.Fatalf("the run failed: %s", res.Content)
	}
	run := runIDs(loop, "conv")[0]

	asks.allow = false
	if out := resume(t, loop, "conv", ""); !strings.HasPrefix(out, "Not resumed") {
		t.Errorf("a refused resume ran or said nothing:\n%s", out)
	}
	if len(asks.asks) != 1 {
		t.Fatalf("asked %d times, want once", len(asks.asks))
	}
	q := asks.asks[0]
	if q.Tool != orchestrateToolName || q.Subject != "resume:"+run || !q.Once || !strings.Contains(q.Description, "fix can change the workspace") {
		t.Errorf("the question is %+v", q)
	}
	if ids := runIDs(loop, "conv"); len(ids) != 1 {
		t.Errorf("a refused resume started a run: %v", ids)
	}

	asks.allow = true
	if out := resume(t, loop, "conv", ""); !strings.Contains(out, "Resumed run "+run) {
		t.Errorf("an allowed resume did not run:\n%s", out)
	}
}

// A readonly run is resumed without a question: it reads what a run of
// it would read, and running it in the first place did not ask either.
func TestResumingAReadonlyRunDoesNotAsk(t *testing.T) {
	loop, _, _, asks := workflowLoop(t, func(string) (string, map[string]any) { return "ok", nil })
	NewOrchestrateTool(loop).Execute(WithSessionID(loop.pinSmart(context.Background()), "conv"),
		json.RawMessage(`{"goal":"g","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"x"}]}`))
	resume(t, loop, "conv", "")
	if len(asks.asks) != 0 {
		t.Errorf("a readonly resume asked: %+v", asks.asks)
	}
}

// A run a /rewind took back is not found by resume or save: the person
// undid it, and a resume of a run that wrote would write it again.
func TestARewoundRunIsNotResumedOrSaved(t *testing.T) {
	m := &scriptedModel{reply: func(string) (string, map[string]any) { return "ok", nil }}
	loop := orchestrateLoop(t, m.server(t).URL)
	executeOrchestrate(context.Background(), loop, `{"goal":"kept","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"x"}]}`)

	turn, _ := loop.Store.Append("main", events.TypeUserMessage, map[string]any{"text": "do it again differently"})
	executeOrchestrate(context.Background(), loop, `{"goal":"undone","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"y"}]}`)
	loop.Store.Append("main", events.TypeRewound, map[string]any{"from_seq": turn.Seq})

	if rec, err := loop.findRun("main", ""); err != nil || rec.plan.Goal != "kept" {
		t.Errorf("resume found %q (%v), want the run before the rewound turn", rec.plan.Goal, err)
	}
	if p, _, ok := loop.lastRunPlan("main"); !ok || p.Goal != "kept" {
		t.Errorf("save found %q, want the run before the rewound turn", p.Goal)
	}
}

// A resume that was let go is still running the run's units, so the run
// is not resumed a second time by its id.
func TestALetGoResumeKeepsTheRunFromBeingResumedAgain(t *testing.T) {
	var slow atomic.Bool
	var started atomic.Int32
	release := make(chan struct{})
	m := &scriptedModel{reply: func(p string) (string, map[string]any) {
		if strings.HasSuffix(p, "second") && slow.Load() {
			started.Add(1)
			select {
			case <-release:
			case <-time.After(5 * time.Second):
			}
		}
		return "ok", nil
	}}
	fail := &sync.Map{}
	fail.Store("second", true)
	loop := orchestrateLoop(t, failingModel(t, m, fail))
	executeOrchestrate(context.Background(), loop, `{"goal":"g","stages":[
	  {"name":"a","kind":"step","agent":"oracle","prompt":"first"},
	  {"name":"b","kind":"step","agent":"oracle","prompt":"second"}]}`)
	run := runIDs(loop, "main")[0]

	fail.Delete("second")
	slow.Store(true)
	resumed := make(chan string, 1)
	go func() { resumed <- resume(t, loop, "main", run) }()
	deadline := time.Now().Add(3 * time.Second)
	for started.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := loop.Tasks.DetachChildOf("main"); !ok {
		t.Fatal("nothing to let go of while the resume ran")
	}
	<-resumed

	if out := resume(t, loop, "main", run); !strings.Contains(out, "is being resumed already, as run task-") {
		t.Errorf("a second resume of a run whose resume was let go:\n%s", out)
	}
	close(release)
}
