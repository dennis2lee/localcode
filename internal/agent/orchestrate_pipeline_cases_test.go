package agent

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"localcode/internal/events"
)

// ceilingPlan reaches the 32-agent ceiling exactly in its second stage, so
// every item of its third stage is past it. pipeline is the third stage's
// pipeline flag.
func ceilingPlan(pipeline bool) string {
	return fmt.Sprintf(`{"goal":"g","stages":[
	  {"name":"find","kind":"step","agent":"oracle","prompt":"find things","returns":{"found":"strings"}},
	  {"name":"look","kind":"fanout","agent":"oracle","over":["$find.found"],"copies":2,"prompt":"look at {{item}}","returns":{"more":"strings"}},
	  {"name":"check","kind":"fanout","agent":"oracle","over":["$look.more"],"copies":2,"pipeline":%v,"prompt":"check {{item}}"},
	  {"name":"sum","kind":"step","agent":"oracle","prompt":"sum up {{input}}"}]}`, pipeline)
}

func ceilingReply(p string) (string, map[string]any) {
	switch {
	case strings.Contains(p, "find things"):
		var out []string
		for i := 1; i <= 16; i++ {
			out = append(out, fmt.Sprintf("f%d", i))
		}
		return "", map[string]any{"found": out}
	case strings.Contains(p, "look at "):
		i := strings.LastIndex(p, "look at ")
		return "", map[string]any{"more": []string{"m-" + p[i+len("look at "):]}}
	}
	return "ok", nil
}

// A stage none of whose items fit under the 32 agent ceiling stops the
// run, pipelined or not, rather than handing the stages after it results
// with a whole stage missing.
func TestAPipelinedStageAtTheCeilingStopsTheRunLikeAnUnpipelinedOne(t *testing.T) {
	for _, pipelined := range []bool{false, true} {
		m := &scriptedModel{reply: ceilingReply}
		loop := orchestrateLoop(t, m.server(t).URL)
		report := runPlanJSON(t, loop, ceilingPlan(pipelined))
		ranSum := false
		for _, s := range report.stages {
			if s.name == "sum" && s.launched > 0 {
				ranSum = true
			}
		}
		check := report.stages[2]
		t.Logf("pipeline=%v: launched=%d stopped=%q check{launched=%d dropped=%d} sum ran=%v",
			pipelined, report.launched, report.stopped, check.launched, check.dropped, ranSum)
		if report.stopped == "" {
			t.Errorf("pipeline=%v: every item of check was past the ceiling (%d dropped), and the run did not stop; sum ran=%v",
				pipelined, check.dropped, ranSum)
		}
	}
}

// A pipeline stopped by unanswered: fail reports the units it launched,
// not every unit it had a place for.
func TestAStoppedPipelineReportsOnlyTheUnitsItLaunched(t *testing.T) {
	m := &scriptedModel{reply: func(p string) (string, map[string]any) {
		if strings.HasSuffix(p, "find in 1") {
			return "no idea", nil
		}
		if strings.Contains(p, "find in") {
			time.Sleep(300 * time.Millisecond)
			return "", map[string]any{"found": []string{"x" + p[len(p)-1:]}}
		}
		return "checked", nil
	}}
	loop := orchestrateLoop(t, m.server(t).URL)
	report := runPlanJSON(t, loop, `{"goal":"g","stages":[
	  {"name":"find","kind":"fanout","agent":"oracle","over":["1","2","3","4","5","6","7","8"],"prompt":"find in {{item}}","returns":{"found":"strings"},"unanswered":"fail"},
	  {"name":"check","kind":"fanout","agent":"oracle","over":["$find.found"],"pipeline":true,"prompt":"check {{item}}"}]}`)
	if report.stopped == "" {
		t.Fatal("the run did not stop")
	}
	evs, _ := loop.Store.Events("main", 0)
	spawned := 0
	for _, e := range evs {
		if e.Type == events.TypeTaskSpawned && e.Data["stage"] == "find" {
			spawned++
		}
	}
	if report.launched != spawned || report.stages[0].launched != spawned {
		t.Errorf("only %d find units were spawned, but the report says the run launched %d and find launched %d:\n%s",
			spawned, report.launched, report.stages[0].launched, report.String())
	}
}
