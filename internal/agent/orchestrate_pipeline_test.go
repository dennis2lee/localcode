package agent

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Pipelining: a fanout over the stage before it starts on an item as soon
// as the unit that found it is done, instead of waiting for the whole
// stage.
//
// Ordered, not as-completed: a unit's items are released once it and every
// unit launched before it have finished. That keeps the first sixteen
// distinct items, the report and the per-unit prompts the same from one run
// to the next, which an as-completed rule would not.

func TestPipelineIsValidatedNarrowly(t *testing.T) {
	chain := planOf(t, `{"goal":"g","stages":[
	  {"name":"find","kind":"fanout","agent":"oracle","over":["x","y"],"prompt":"{{item}}","returns":{"found":"strings"}},
	  {"name":"check","kind":"fanout","agent":"oracle","over":["$find.found"],"pipeline":true,"prompt":"check {{item}}","returns":{"notes":"strings"}},
	  {"name":"fix","kind":"fanout","agent":"oracle","over":["$check.notes"],"pipeline":true,"prompt":"{{item}}"}]}`)
	if err := chain.Validate(roster("oracle")); err != nil {
		t.Fatalf("a pipelined chain was refused: %v", err)
	}
	for _, tc := range []struct {
		name string
		plan string
		want string
	}{
		{"on a step", `{"goal":"g","stages":[
			{"name":"find","kind":"fanout","agent":"oracle","over":["x"],"prompt":"{{item}}","returns":{"found":"strings"}},
			{"name":"b","kind":"step","agent":"oracle","pipeline":true,"prompt":"x"}]}`,
			"pipeline belongs to a fanout"},
		{"over a literal list", `{"goal":"g","stages":[
			{"name":"find","kind":"fanout","agent":"oracle","over":["x"],"prompt":"{{item}}","returns":{"found":"strings"}},
			{"name":"b","kind":"fanout","agent":"oracle","over":["p","q"],"pipeline":true,"prompt":"{{item}}"}]}`,
			"the stage just before it"},
		{"over a stage further back", `{"goal":"g","stages":[
			{"name":"find","kind":"fanout","agent":"oracle","over":["x"],"prompt":"{{item}}","returns":{"found":"strings"}},
			{"name":"mid","kind":"fanout","agent":"oracle","over":["y"],"prompt":"{{item}}"},
			{"name":"b","kind":"fanout","agent":"oracle","over":["$find.found"],"pipeline":true,"prompt":"{{item}}"}]}`,
			"the stage just before it"},
		{"after a step", `{"goal":"g","stages":[
			{"name":"find","kind":"step","agent":"oracle","prompt":"x","returns":{"found":"strings"}},
			{"name":"b","kind":"fanout","agent":"oracle","over":["$find.found"],"pipeline":true,"prompt":"{{item}}"}]}`,
			"after a fanout"},
		{"with {{input}}", `{"goal":"g","stages":[
			{"name":"find","kind":"fanout","agent":"oracle","over":["x"],"prompt":"{{item}}","returns":{"found":"strings"}},
			{"name":"b","kind":"fanout","agent":"oracle","over":["$find.found"],"pipeline":true,"prompt":"{{item}} {{input}}"}]}`,
			"{{input}}"},
		{"after a repeating stage", `{"goal":"g","stages":[
			{"name":"find","kind":"fanout","agent":"oracle","over":["x"],"prompt":"{{item}}","returns":{"found":"strings"},"repeat_until_dry":"found","max_rounds":3},
			{"name":"b","kind":"fanout","agent":"oracle","over":["$find.found"],"pipeline":true,"prompt":"{{item}}"}]}`,
			"repeats"},
		{"repeating itself", `{"goal":"g","stages":[
			{"name":"find","kind":"fanout","agent":"oracle","over":["x"],"prompt":"{{item}}","returns":{"found":"strings"}},
			{"name":"b","kind":"fanout","agent":"oracle","over":["$find.found"],"pipeline":true,"prompt":"{{item}}",
			 "returns":{"more":"strings"},"repeat_until_dry":"more","max_rounds":3}]}`,
			"repeats"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := planOf(t, tc.plan).Validate(roster("oracle"))
			if err == nil {
				t.Fatal("the plan was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

// The point of it: the second stage starts while the first is still
// running, and everything it would have done without pipelining still
// happens.
func TestAPipelinedStageStartsBeforeThePreviousStageEnds(t *testing.T) {
	var mu sync.Mutex
	firstCheck := time.Time{}
	lastFind := time.Time{}
	m := &scriptedModel{}
	m.reply = func(prompt string) (string, map[string]any) {
		switch {
		case strings.Contains(prompt, "find in"):
			if strings.Contains(prompt, "slow") {
				time.Sleep(900 * time.Millisecond)
			}
			mu.Lock()
			lastFind = time.Now()
			mu.Unlock()
			item := prompt[strings.Index(prompt, "find in ")+len("find in "):]
			return "", map[string]any{"found": []string{"issue in " + item}}
		case strings.Contains(prompt, "check"):
			mu.Lock()
			if firstCheck.IsZero() {
				firstCheck = time.Now()
			}
			mu.Unlock()
			return "", map[string]any{"real": !strings.Contains(prompt, "fake")}
		}
		return "?", nil
	}
	loop := orchestrateLoop(t, m.server(t).URL)
	report := runPlanJSON(t, loop, `{"goal":"g","stages":[
	  {"name":"find","kind":"fanout","agent":"oracle","over":["quick","fake","slow"],"prompt":"find in {{item}}","returns":{"found":"strings"}},
	  {"name":"check","kind":"fanout","agent":"oracle","over":["$find.found"],"pipeline":true,
	   "prompt":"check {{item}}","returns":{"real":"bool"},"keep":"real"}]}`)

	if report.stopped != "" {
		t.Fatalf("the run did not finish: %s", report.stopped)
	}
	mu.Lock()
	early := firstCheck.Before(lastFind)
	mu.Unlock()
	if !early {
		t.Error("the pipelined stage did not start until every unit of the stage before it had finished")
	}
	c := report.stages[1]
	if c.launched != 3 || c.kept != 2 {
		t.Errorf("check launched %d and kept %d, want 3 and 2: pipelining must not change what runs", c.launched, c.kept)
	}
	if out := report.String(); !strings.Contains(out, "pipelined") {
		t.Errorf("the report does not say the stage was pipelined:\n%s", out)
	}
}

// Ordered release: a fast unit's items wait for the units launched before
// it, so the items, their order and the report are the same every run.
func TestPipelinedItemsKeepLaunchOrder(t *testing.T) {
	m := &scriptedModel{}
	m.reply = func(prompt string) (string, map[string]any) {
		if strings.Contains(prompt, "find in") {
			// The first unit is the slow one.
			if strings.Contains(prompt, "find in a") {
				time.Sleep(400 * time.Millisecond)
			}
			item := prompt[strings.Index(prompt, "find in ")+len("find in "):]
			return "", map[string]any{"found": []string{"from " + item}}
		}
		return "checked", nil
	}
	loop := orchestrateLoop(t, m.server(t).URL)
	report := runPlanJSON(t, loop, `{"goal":"g","stages":[
	  {"name":"find","kind":"fanout","agent":"oracle","over":["a","b","c"],"prompt":"find in {{item}}","returns":{"found":"strings"}},
	  {"name":"check","kind":"fanout","agent":"oracle","over":["$find.found"],"pipeline":true,"prompt":"check {{item}}"}]}`)

	var order []string
	for _, o := range report.stages[1].answers {
		order = append(order, o.item)
	}
	if got := strings.Join(order, ","); got != "from a,from b,from c" {
		t.Errorf("pipelined items ran in the order %q, want launch order", got)
	}
}

// The run ceiling still holds with two stages launching at once, and what
// it cut is counted.
func TestAPipelineRespectsTheRunCeiling(t *testing.T) {
	var checks int32
	m := &scriptedModel{reply: func(prompt string) (string, map[string]any) {
		if strings.Contains(prompt, "find in") {
			n := prompt[len(prompt)-1:]
			var out []string
			for i := 0; i < 8; i++ {
				out = append(out, "item "+n+string(rune('a'+i)))
			}
			return "", map[string]any{"found": out}
		}
		atomic.AddInt32(&checks, 1)
		return "ok", nil
	}}
	loop := orchestrateLoop(t, m.server(t).URL)
	// 4 finders, each finding 8 distinct items, 2 copies each: far past 32.
	report := runPlanJSON(t, loop, `{"goal":"g","stages":[
	  {"name":"find","kind":"fanout","agent":"oracle","over":["1","2","3","4"],"prompt":"find in {{item}}","returns":{"found":"strings"}},
	  {"name":"check","kind":"fanout","agent":"oracle","over":["$find.found"],"pipeline":true,"copies":2,"prompt":"check {{item}}"}]}`)

	if report.launched > maxRunAgents {
		t.Errorf("the run launched %d agents, past the ceiling of %d", report.launched, maxRunAgents)
	}
	if got := atomic.LoadInt32(&checks); int(got) != report.stages[1].launched {
		t.Errorf("%d check turns reached the model but the report counts %d", got, report.stages[1].launched)
	}
	if c := report.stages[1]; c.dropped == 0 {
		t.Error("the items the ceiling cut were not counted")
	}
}

// A stage that says unanswered: fail stops the pipelined stage after it,
// including units already in flight.
func TestAFailingProducerStopsThePipeline(t *testing.T) {
	m := &scriptedModel{reply: func(prompt string) (string, map[string]any) {
		if strings.Contains(prompt, "find in") {
			if strings.Contains(prompt, "find in bad") {
				time.Sleep(200 * time.Millisecond)
				return "no idea", nil
			}
			return "", map[string]any{"found": []string{"one finding"}}
		}
		time.Sleep(1500 * time.Millisecond)
		return "checked", nil
	}}
	loop := orchestrateLoop(t, m.server(t).URL)
	began := time.Now()
	report := runPlanJSON(t, loop, `{"goal":"g","stages":[
	  {"name":"find","kind":"fanout","agent":"oracle","over":["good","bad"],"prompt":"find in {{item}}","returns":{"found":"strings"},"unanswered":"fail"},
	  {"name":"check","kind":"fanout","agent":"oracle","over":["$find.found"],"pipeline":true,"prompt":"check {{item}}"}]}`)

	if report.stopped == "" {
		t.Fatal("the run finished although a stage said unanswered: fail")
	}
	if elapsed := time.Since(began); elapsed > 1200*time.Millisecond {
		t.Errorf("the run took %v to stop: the pipelined unit in flight was not cancelled", elapsed)
	}
}
