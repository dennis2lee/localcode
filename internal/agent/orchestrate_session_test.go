package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"localcode/internal/events"
	"localcode/internal/tools"
)

// A run is one task of the conversation that started it.
//
// Everything that stops, deletes, archives, drains or lets go of work in
// this repository does it by session id, so a run that was only a loop
// inside a tool call was invisible to all of them. These pin the run as a
// session: one row, its units underneath it, and the whole run the thing
// that let go and Esc act on.

// slowFanout is a plan whose four units each take long enough for a test
// to act while they are in flight.
const slowFanout = `{"goal":"look at four things","stages":[
  {"name":"look","kind":"fanout","agent":"oracle","over":["a","b","c","d"],"prompt":"look at {{item}}"}]}`

func executeOrchestrate(ctx context.Context, loop *Loop, plan string) tools.Result {
	return NewOrchestrateTool(loop).Execute(WithSessionID(loop.pinSmart(ctx), "main"), json.RawMessage(plan))
}

// Letting go during a run must let go of the run.
//
// It used to pick one of the run's units at random: up to four synchronous
// children shared the conversation as their parent, the run recorded the
// let-go note as that unit's answer, and the turn stayed blocked on the
// other three.
func TestLettingGoOfARunLetsGoOfTheWholeRun(t *testing.T) {
	var started int32
	m := &scriptedModel{reply: func(string) (string, map[string]any) {
		atomic.AddInt32(&started, 1)
		time.Sleep(1500 * time.Millisecond)
		return "looked", nil
	}}
	loop := orchestrateLoop(t, m.server(t).URL)

	done := make(chan tools.Result, 1)
	go func() { done <- executeOrchestrate(context.Background(), loop, slowFanout) }()

	deadline := time.Now().Add(3 * time.Second)
	for atomic.LoadInt32(&started) < 4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&started) < 4 {
		t.Fatalf("only %d units started", atomic.LoadInt32(&started))
	}

	let, ok := letGoOf(loop.Tasks, "main")
	if !ok {
		t.Fatal("nothing to let go of while a run was in flight")
	}

	select {
	case res := <-done:
		if !strings.Contains(res.Content, let) {
			t.Errorf("the turn got back something other than the let-go note for %s:\n%s", let, res.Content)
		}
	case <-time.After(700 * time.Millisecond):
		t.Fatal("letting go did not give the turn back: the other units still held it")
	}

	sess, err := loop.Store.Get(let)
	if err != nil {
		t.Fatalf("the let-go task %s is not a session: %v", let, err)
	}
	if sess.Agent != runSessionAgent {
		t.Errorf("let go took a %q task, want the run itself (%q)", sess.Agent, runSessionAgent)
	}

	// The run carries on without the turn, and its report lands in the run.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(lastAssistantText(loop.Store, let), "Orchestration finished") {
		time.Sleep(20 * time.Millisecond)
	}
	if got := lastAssistantText(loop.Store, let); !strings.Contains(got, "4 agent turns") {
		t.Errorf("the let-go run did not finish with its report in the run session:\n%s", got)
	}

	// Once let go, the run's progress is its own: the stage that finished
	// afterwards reports in the run, not in a conversation that has moved on.
	finished := func(sessionID string) bool {
		evs, _ := loop.Store.Events(sessionID, 0)
		for _, e := range evs {
			if e.Type == events.TypeTaskStatus && e.Data["task_id"] == "orchestrate:look" && e.Data["status"] == "completed" {
				return true
			}
		}
		return false
	}
	if finished("main") {
		t.Error("a let-go run wrote its stage progress into the conversation")
	}
	if !finished(let) {
		t.Error("a let-go run's stage progress is not in the run")
	}
}

// The run is one task row of the conversation, and its units are its own
// children rather than the conversation's.
func TestARunIsOneTaskWithItsUnitsUnderIt(t *testing.T) {
	m := &scriptedModel{reply: func(string) (string, map[string]any) { return "ok", nil }}
	loop := orchestrateLoop(t, m.server(t).URL)

	res := executeOrchestrate(context.Background(), loop, `{"goal":"g","stages":[
	  {"name":"a","kind":"fanout","agent":"oracle","over":["1","2"],"prompt":"{{item}}"},
	  {"name":"b","kind":"step","agent":"plan","prompt":"after"}]}`)
	if res.IsError {
		t.Fatalf("the run failed: %s", res.Content)
	}

	kids := loop.Store.Children("main")
	if len(kids) != 1 {
		t.Fatalf("the conversation has %d children, want the one run: %v", len(kids), kids)
	}
	run := kids[0].ID
	if kids[0].Agent != runSessionAgent {
		sess := kids[0]
		t.Errorf("the conversation's child is a %q session, want the run", sess.Agent)
	}
	if units := loop.Store.Children(run); len(units) != 3 {
		t.Errorf("the run has %d units under it, want 3", len(units))
	}
	if got := lastAssistantText(loop.Store, run); !strings.Contains(got, "Orchestration finished") {
		t.Errorf("the run session does not hold the report:\n%s", got)
	}

	// The stage progress lines stay in the conversation, where the person
	// is watching.
	evs, _ := loop.Store.Events("main", 0)
	stages := 0
	for _, e := range evs {
		if e.Type == events.TypeTaskStatus {
			if id, _ := e.Data["task_id"].(string); strings.HasPrefix(id, "orchestrate:") {
				stages++
			}
		}
	}
	if stages == 0 {
		t.Error("the conversation lost its stage progress lines")
	}
}

// Esc on the turn stops every unit, through the run.
func TestCancellingTheTurnStopsTheRunSession(t *testing.T) {
	var started int32
	m := &scriptedModel{reply: func(string) (string, map[string]any) {
		atomic.AddInt32(&started, 1)
		time.Sleep(2 * time.Second)
		return "slow", nil
	}}
	loop := orchestrateLoop(t, m.server(t).URL)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for atomic.LoadInt32(&started) < 4 {
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	began := time.Now()
	res := executeOrchestrate(ctx, loop, slowFanout)
	if elapsed := time.Since(began); elapsed > 1800*time.Millisecond {
		t.Errorf("the run took %v to stop after Esc", elapsed)
	}
	if !res.IsError {
		t.Errorf("a cancelled run reported success:\n%s", res.Content)
	}
	if n := len(loop.Tasks.Running()); n != 0 {
		t.Errorf("%d tasks still running after the run was stopped", n)
	}
}

// Nobody types into a run: it is not a conversation with a model.
func TestARunSessionTakesNoMessages(t *testing.T) {
	m := &scriptedModel{reply: func(string) (string, map[string]any) { return "ok", nil }}
	loop := orchestrateLoop(t, m.server(t).URL)
	executeOrchestrate(context.Background(), loop, `{"goal":"g","stages":[
	  {"name":"a","kind":"step","agent":"oracle","prompt":"x"}]}`)
	run := loop.Store.Children("main")[0].ID

	before := atomic.LoadInt32(&m.turns)
	if err := loop.SendMessage(context.Background(), run, "oracle", "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if atomic.LoadInt32(&m.turns) != before {
		t.Error("a message typed into a run session reached a model")
	}
	evs, _ := loop.Store.Events(run, 0)
	last := evs[len(evs)-1]
	if last.Type != events.TypeError {
		t.Errorf("the run session's answer was a %s, want an error saying it takes no messages", last.Type)
	}
}

// runProgressOf runs plan and returns every progress line the
// conversation was sent for the run's row.
func runProgressOf(t *testing.T, reply func(string) (string, map[string]any), plan string) []string {
	t.Helper()
	m := &scriptedModel{reply: reply}
	loop := orchestrateLoop(t, m.server(t).URL)
	ch, _, unsub, err := loop.Store.Subscribe("main")
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var doing []string
	var ids []string
	collected := make(chan struct{})
	go func() {
		defer close(collected)
		for ev := range ch {
			if ev.Type != events.TypeTaskProgress {
				continue
			}
			if ev.Seq != 0 {
				t.Errorf("run progress was written to the log (seq %d)", ev.Seq)
			}
			mu.Lock()
			doing = append(doing, ev.Data["doing"].(string))
			ids = append(ids, ev.Data["task_id"].(string))
			mu.Unlock()
		}
	}()
	executeOrchestrate(context.Background(), loop, plan)
	run := loop.Store.Children("main")[0].ID
	unsub()
	<-collected

	mu.Lock()
	defer mu.Unlock()
	for _, id := range ids {
		if id != run {
			t.Errorf("progress for %s, want it on the run's row %s", id, run)
		}
	}
	return doing
}

// A run's row says how far it has got: the stage, and how many of its
// agents have finished, on the run's own row in the conversation.
func TestARunsRowSaysHowFarItHasGot(t *testing.T) {
	doing := runProgressOf(t, func(string) (string, map[string]any) { return "ok", nil }, `{"goal":"g","stages":[
	  {"name":"look","kind":"fanout","agent":"oracle","over":["a","b"],"prompt":"look at {{item}}"},
	  {"name":"sum","kind":"step","agent":"oracle","prompt":"sum up {{input}}"}]}`)
	for _, want := range []string{"look: 0 of 2 finished", "look: 2 of 2 finished", "sum: 0 of 1 finished", "sum: 1 of 1 finished"} {
		if !slices.Contains(doing, want) {
			t.Errorf("the run's row never said %q; it said %q", want, doing)
		}
	}
}

// A pipelined stage counts against the units it has so far.
func TestAPipelinedRunsRowCountsBothStages(t *testing.T) {
	doing := runProgressOf(t, func(p string) (string, map[string]any) {
		if strings.Contains(p, "find in") {
			return "found", map[string]any{"found": []any{"x", "y"}}
		}
		return "checked", nil
	}, `{"goal":"g","stages":[
	  {"name":"find","kind":"fanout","agent":"oracle","over":["a"],"prompt":"find in {{item}}","returns":{"found":"strings"}},
	  {"name":"check","kind":"fanout","agent":"oracle","over":["$find.found"],"pipeline":true,"prompt":"check {{item}}"}]}`)
	for _, want := range []string{"find: 0 of 1 finished", "find: 1 of 1 finished", "check: 2 of 2 finished"} {
		if !slices.Contains(doing, want) {
			t.Errorf("the run's row never said %q; it said %q", want, doing)
		}
	}
}

// Letting go from inside a run lets go of the run, never of one of its
// units: a unit let go on its own outlives Esc and the run's deadline,
// and a resume would start it again beside itself. Asked with the run's
// id (the terminal sends whatever session it shows), it is the run.
func TestLettingGoFromInsideARunLetsGoOfTheRun(t *testing.T) {
	var started int32
	m := &scriptedModel{reply: func(string) (string, map[string]any) {
		atomic.AddInt32(&started, 1)
		time.Sleep(800 * time.Millisecond)
		return "looked", nil
	}}
	loop := orchestrateLoop(t, m.server(t).URL)
	done := make(chan tools.Result, 1)
	go func() { done <- executeOrchestrate(context.Background(), loop, slowFanout) }()
	deadline := time.Now().Add(3 * time.Second)
	for atomic.LoadInt32(&started) < 4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	run := loop.Store.Children("main")[0].ID

	let, ok := loop.Tasks.DetachChildOf(run)
	if !ok || len(let) != 1 || let[0] != run {
		t.Fatalf("letting go inside run %s let go of %q (%v), want the run", run, let, ok)
	}
	select {
	case res := <-done:
		if res.IsError || !strings.Contains(res.Content, "still working") {
			t.Errorf("the turn was not handed back with the let-go note:\n%s", res.Content)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("the turn was still waiting after the run was let go")
	}
	deadline = time.Now().Add(5 * time.Second)
	for len(loop.Tasks.Running()) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
}
