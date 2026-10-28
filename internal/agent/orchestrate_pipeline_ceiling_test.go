package agent

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// A pipelined chain that reaches the ceiling runs the same items whichever
// agent is slow: the items the same plan runs without pipelining. A resume
// matches units by what they were asked, so this is what makes a pipelined
// run resumable at all.
func TestAPipelinedChainAtTheCeilingRunsTheSameItemsWhateverIsSlow(t *testing.T) {
	const plan = `{"goal":"g","stages":[
	  {"name":"find","kind":"fanout","agent":"oracle","over":["a","b"],"prompt":"find in {{item}}","returns":{"found":"strings"}},
	  {"name":"check","kind":"fanout","agent":"oracle","over":["$find.found"],"pipeline":true,"prompt":"check {{item}}","returns":{"more":"strings"}},
	  {"name":"deep","kind":"fanout","agent":"oracle","over":["$check.more"],"pipeline":true,"prompt":"deep {{item}}"}]}`

	run := func(slowFindB, slowCheck bool) string {
		m := &scriptedModel{reply: func(p string) (string, map[string]any) {
			switch {
			case strings.HasSuffix(p, "find in a"), strings.HasSuffix(p, "find in b"):
				x := p[len(p)-1:]
				if x == "b" && slowFindB {
					time.Sleep(1200 * time.Millisecond)
				}
				var found []any
				for i := 1; i <= 8; i++ {
					found = append(found, fmt.Sprintf("%s%d", x, i))
				}
				return "found", map[string]any{"found": found}
			case strings.Contains(p, "check "):
				item := p[strings.LastIndex(p, "check ")+len("check "):]
				if slowCheck {
					time.Sleep(250 * time.Millisecond)
				}
				return "checked", map[string]any{"more": []any{item + "-1", item + "-2"}}
			}
			return "ok", nil
		}}
		loop := orchestrateLoop(t, m.server(t).URL)
		r := runPlanJSON(t, loop, plan)
		var b strings.Builder
		for _, s := range r.stages {
			var items []string
			for _, o := range s.answers {
				items = append(items, o.item)
			}
			fmt.Fprintf(&b, "%s: launched %d, dropped %d, items %v\n", s.name, s.launched, s.dropped, items)
		}
		return b.String()
	}

	slowProducer := run(true, false)
	slowChecker := run(false, true)
	if slowProducer != slowChecker {
		t.Errorf("the same plan with the same answers ran different items:\nwith find b slow:\n%s\nwith check slow:\n%s",
			slowProducer, slowChecker)
	}
}

// A pipeline whose first stage has no room left under the ceiling stops
// the run, as an ordinary stage does, rather than handing the stages after
// it results with a whole stage missing.
func TestAPipelineHeadWithNoRoomStopsTheRun(t *testing.T) {
	// seed: 3 turns, 24 items. wide: 14 of them at 2 copies, 28 turns, so
	// the run is at 31. head (2 copies): 14 items, room for none. sum: one
	// turn, which fits.
	plan := func(pipeline bool) string {
		return fmt.Sprintf(`{"goal":"g","stages":[
	  {"name":"seed","kind":"fanout","agent":"oracle","over":["s1","s2","s3"],"prompt":"seed {{item}}","returns":{"found":"strings"}},
	  {"name":"wide","kind":"fanout","agent":"oracle","over":["$seed.found"],"copies":2,"prompt":"find in {{item}}","returns":{"found":"strings"}},
	  {"name":"head","kind":"fanout","agent":"oracle","over":["$wide.found"],"copies":2,"prompt":"look at {{item}}","returns":{"more":"strings"}},
	  {"name":"tail","kind":"fanout","agent":"oracle","over":["$head.more"],"pipeline":%v,"prompt":"tail {{item}}"},
	  {"name":"sum","kind":"step","agent":"oracle","prompt":"sum up {{input}}"}]}`, pipeline)
	}
	reply := func(p string) (string, map[string]any) {
		if i := strings.LastIndex(p, "seed "); i >= 0 {
			s := p[i+len("seed "):]
			var found []any
			for k := 1; k <= 8; k++ {
				found = append(found, fmt.Sprintf("%s-%d", s, k))
			}
			return "seeded", map[string]any{"found": found}
		}
		if i := strings.LastIndex(p, "find in "); i >= 0 {
			return "found", map[string]any{"found": []any{"w " + p[i+len("find in "):]}}
		}
		return "ok", map[string]any{"more": []any{"m"}}
	}
	stopped := func(pipeline bool) (string, string) {
		m := &scriptedModel{reply: reply}
		loop := orchestrateLoop(t, m.server(t).URL)
		r := runPlanJSON(t, loop, plan(pipeline))
		var last string
		if len(r.stages) > 0 {
			s := r.stages[len(r.stages)-1]
			last = fmt.Sprintf("%s: %d launched", s.name, s.launched)
		}
		return r.stopped, last
	}
	plain, plainLast := stopped(false)
	piped, pipedLast := stopped(true)
	if plain == "" {
		t.Fatalf("the unpipelined plan did not stop; the test plan is wrong")
	}
	if piped == "" {
		t.Errorf("without pipeline the run stops (%q, last stage %s); with tail pipelined it does not stop, "+
			"head never ran, and the last stage is %s", plain, plainLast, pipedLast)
	}
}

// A pipelined stage counts what it launches and drops the way an ordinary
// fanout over the same results does.
func TestAPipelinedStageCountsDroppedItemsAsAnOrdinaryFanoutDoes(t *testing.T) {
	plan := func(pipeline bool) string {
		var over []string
		for i := 1; i <= 16; i++ {
			over = append(over, fmt.Sprintf("%q", fmt.Sprintf("f%02d", i)))
		}
		return fmt.Sprintf(`{"goal":"g","stages":[
	  {"name":"warm","kind":"step","agent":"oracle","prompt":"warm up"},
	  {"name":"find","kind":"fanout","agent":"oracle","over":[%s],"prompt":"find in {{item}}","returns":{"found":"strings"}},
	  {"name":"check","kind":"fanout","agent":"oracle","over":["$find.found"],"pipeline":%v,"prompt":"check {{item}}"}]}`,
			strings.Join(over, ","), pipeline)
	}
	reply := func(p string) (string, map[string]any) {
		if i := strings.LastIndex(p, "find in "); i >= 0 {
			x := p[i+len("find in "):]
			return "found", map[string]any{"found": []any{x + "-1", x + "-2"}}
		}
		return "ok", nil
	}
	dropped := func(pipeline bool) (int, int) {
		m := &scriptedModel{reply: reply}
		loop := orchestrateLoop(t, m.server(t).URL)
		r := runPlanJSON(t, loop, plan(pipeline))
		for _, s := range r.stages {
			if s.name == "check" {
				return s.launched, s.dropped
			}
		}
		t.Fatalf("no check stage in the report")
		return 0, 0
	}
	plainRan, plainDropped := dropped(false)
	pipeRan, pipeDropped := dropped(true)
	if plainRan != pipeRan || plainDropped != pipeDropped {
		t.Errorf("check without pipeline: %d launched, %d dropped; with pipeline: %d launched, %d dropped",
			plainRan, plainDropped, pipeRan, pipeDropped)
	}
}
