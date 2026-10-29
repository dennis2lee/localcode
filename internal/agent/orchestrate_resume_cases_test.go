package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A resume that was let go goes on running the units it did not reuse.
// A second resume of the same run must not start them again beside it.
func TestALetGoResumeDoesNotRunTheSameUnitsTwice(t *testing.T) {
	var slow atomic.Bool
	var inFlight, maxInFlight atomic.Int32
	m := &scriptedModel{reply: func(p string) (string, map[string]any) {
		if strings.Contains(p, "sum up") {
			return "summed", nil
		}
		if i := strings.LastIndex(p, "look at "); i >= 0 {
			item := p[i+len("look at "):]
			if item == "d" && slow.Load() {
				n := inFlight.Add(1)
				for {
					old := maxInFlight.Load()
					if n <= old || maxInFlight.CompareAndSwap(old, n) {
						break
					}
				}
				time.Sleep(1500 * time.Millisecond)
				inFlight.Add(-1)
			}
			return "looked at " + item, map[string]any{"seen": item}
		}
		return "summed", nil
	}}
	fail := &sync.Map{}
	fail.Store("look at d", true)
	loop := orchestrateLoop(t, failingModel(t, m, fail))

	executeOrchestrate(context.Background(), loop, lookThenSum)
	original := runIDs(loop, "main")[0]

	fail.Delete("look at d")
	slow.Store(true)

	// First resume, let go while its "look at d" unit is in flight.
	firstDone := make(chan string, 1)
	go func() {
		loop.SendMessage(context.Background(), "main", "oracle", "/workflow resume "+original)
		firstDone <- lastAssistantText(loop.Store, "main")
	}()
	deadline := time.Now().Add(5 * time.Second)
	for inFlight.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if inFlight.Load() == 0 {
		t.Fatal("the resumed unit never started")
	}
	letGo, ok := loop.Tasks.DetachChildOf("main")
	if !ok {
		t.Fatal("nothing to let go of")
	}
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("letting go did not return the first resume")
	}

	// Second resume of the same run, while the first is still running.
	out := resume(t, loop, "main", original)
	if !strings.Contains(out, "Not resumed") {
		t.Errorf("a second resume of %s started while the first (%s, let go) was still running it:\n%s",
			original, letGo, firstLine(out))
	}
	if n := maxInFlight.Load(); n > 1 {
		t.Errorf("the unit \"look at d\" of run %s ran %d times at once", original, n)
	}

	// Let the let-go resume finish before the test's servers close.
	deadline = time.Now().Add(5 * time.Second)
	for len(loop.Tasks.Running()) > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
}

// The items of a fanout whose prompt does not say {{item}} all have the
// same prompt. Each keeps its own answer through a resume, because the item
// is in the key.
func TestAResumeKeepsEveryItemsOwnAnswer(t *testing.T) {
	var n atomic.Int32
	m := &scriptedModel{reply: func(p string) (string, map[string]any) {
		if strings.Contains(p, "sum up") {
			return "summed", nil
		}
		if strings.Contains(p, "look around") {
			k := n.Add(1)
			return fmt.Sprintf("answer-%d", k), map[string]any{"seen": fmt.Sprintf("answer-%d", k)}
		}
		return "?", nil
	}}
	fail := &sync.Map{}
	fail.Store("sum up", true)
	loop := orchestrateLoop(t, failingModel(t, m, fail))

	plan := `{"goal":"g","stages":[
	  {"name":"look","kind":"fanout","agent":"oracle","over":["a","b"],"prompt":"look around","returns":{"seen":"string"}},
	  {"name":"sum","kind":"step","agent":"oracle","prompt":"sum up {{input}}"}]}`
	first := executeOrchestrate(context.Background(), loop, plan)
	for _, want := range []string{"answer-1", "answer-2"} {
		if !strings.Contains(first.Content, want) {
			t.Fatalf("the first run's report lacks %s:\n%s", want, first.Content)
		}
	}

	fail.Delete("sum up")
	before := len(m.seen())
	out := resume(t, loop, "main", "")
	var sumPrompt string
	for _, p := range m.seen()[before:] {
		if strings.Contains(p, "sum up") {
			sumPrompt = p
		}
	}
	for _, want := range []string{"answer-1", "answer-2"} {
		if !strings.Contains(out, want) {
			t.Errorf("the resumed report lost %s, which one of the original run's two look units answered:\n%s", want, out)
		}
		if !strings.Contains(sumPrompt, want) {
			t.Errorf("the resumed sum stage was not given %s; its prompt:\n%s", want, sumPrompt)
		}
	}
}

// An answer about one checkout says nothing about another: after
// /workspace moves the conversation, a resume runs every unit again.
func TestAResumeDoesNotReuseAnswersFromAnotherWorkspace(t *testing.T) {
	m := &scriptedModel{reply: func(p string) (string, map[string]any) {
		if strings.Contains(p, "sum up") {
			return "summed", nil
		}
		if i := strings.LastIndex(p, "look at "); i >= 0 {
			item := p[i+len("look at "):]
			return "looked at " + item, map[string]any{"seen": item}
		}
		return "summed", nil
	}}
	fail := &sync.Map{}
	fail.Store("sum up", true)
	loop := orchestrateLoop(t, failingModel(t, m, fail))
	dirA, dirB := t.TempDir(), t.TempDir()
	if _, err := loop.Store.SetWorkspace("main", dirA); err != nil {
		t.Fatal(err)
	}

	executeOrchestrate(context.Background(), loop, lookThenSum)
	fail.Delete("sum up")

	if _, err := loop.Store.SetWorkspace("main", dirB); err != nil {
		t.Fatal(err)
	}
	before := len(m.seen())
	resume(t, loop, "main", "")
	asked := 0
	for _, p := range m.seen()[before:] {
		if strings.Contains(p, "\n\nlook at ") {
			asked++
		}
	}
	runs := runIDs(loop, "main")
	var resumed string
	for _, id := range runs {
		if sess, _ := loop.Store.Get(id); sess != nil && sess.Workspace == dirB {
			resumed = id
		}
	}
	if resumed == "" {
		t.Fatalf("no run in %s among %v", dirB, runs)
	}
	reused := loop.runJournal(resumed)
	for _, c := range reused {
		child, err := loop.Store.Get(c.child)
		if err != nil {
			continue
		}
		if child.Workspace != dirB {
			t.Errorf("run %s works in %s but reused unit %s's answer %v, produced in %s (%d look units asked again)",
				resumed, dirB, c.child, c.data, child.Workspace, asked)
			break
		}
	}
}

// A run that stopped at the 32 agent turn ceiling is not resumed: the
// answers a resume reuses count toward the same ceiling, so it would stop
// at the same place, every time. The refusal says so.
func TestARunStoppedAtTheCeilingIsNotResumed(t *testing.T) {
	m := &scriptedModel{reply: func(p string) (string, map[string]any) {
		if strings.Contains(p, "find things") {
			var out []any
			for i := 1; i <= 20; i++ {
				out = append(out, fmt.Sprintf("f%d", i))
			}
			return "", map[string]any{"found": out}
		}
		return "ok", nil
	}}
	loop := orchestrateLoop(t, m.server(t).URL)
	res := executeOrchestrate(context.Background(), loop, `{"goal":"g","stages":[
	  {"name":"find","kind":"step","agent":"oracle","prompt":"find things","returns":{"found":"strings"}},
	  {"name":"check","kind":"fanout","agent":"oracle","over":["$find.found"],"copies":2,"prompt":"check {{item}}"},
	  {"name":"last","kind":"fanout","agent":"oracle","over":["x","y"],"prompt":"last {{item}}"}]}`)
	if !strings.Contains(res.Content, "past the limit") {
		t.Fatalf("the run did not stop at the ceiling:\n%s", res.Content)
	}
	before := len(m.seen())
	out := resume(t, loop, "main", "")
	if !strings.Contains(out, "Not resumed") || !strings.Contains(out, "ceiling") {
		t.Errorf("a run stopped at the ceiling was resumed, or refused without saying why:\n%s", out)
	}
	if n := len(m.seen()) - before; n != 0 {
		t.Errorf("the refused resume asked a model %d times", n)
	}
}

// Letting go names a task only when one was let go: asked about a run
// that has already finished, it names nothing.
func TestLettingGoOfAFinishedRunNamesNothing(t *testing.T) {
	m := &scriptedModel{reply: func(string) (string, map[string]any) { return "ok", nil }}
	loop := orchestrateLoop(t, m.server(t).URL)
	executeOrchestrate(context.Background(), loop, `{"goal":"g","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"x"}]}`)
	run := runIDs(loop, "main")[0]
	if id, ok := loop.Tasks.DetachChildOf(run); ok || id != "" {
		t.Errorf("DetachChildOf(finished run) = (%q, %v), want nothing", id, ok)
	}
}
