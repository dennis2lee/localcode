package agent

import (
	"fmt"
	"strings"
	"testing"
)

// A fanout over an earlier stage's results takes the first 16 distinct
// items, and the report counts the rest rather than dropping them
// silently.
func TestTheReportNamesItemsPastTheSixteenItemCap(t *testing.T) {
	for _, pipeline := range []bool{false, true} {
		m := &scriptedModel{reply: func(p string) (string, map[string]any) {
			if strings.Contains(p, "find things") {
				var out []string
				for i := 1; i <= 20; i++ {
					out = append(out, fmt.Sprintf("f%d", i))
				}
				return "", map[string]any{"found": out}
			}
			return "checked", nil
		}}
		loop := orchestrateLoop(t, m.server(t).URL)
		report := runPlanJSON(t, loop, fmt.Sprintf(`{"goal":"g","stages":[
		  {"name":"find","kind":"fanout","agent":"oracle","over":["a"],"prompt":"find things in {{item}}","returns":{"found":"strings"}},
		  {"name":"check","kind":"fanout","agent":"oracle","over":["$find.found"],"pipeline":%v,"prompt":"check {{item}}"}]}`, pipeline))
		c := report.stages[1]
		out := report.String()
		if c.launched != 16 || c.capped != 4 || !strings.Contains(out, "4 item(s) past the first 16 not run") {
			t.Errorf("pipeline=%v, 20 findings: check launched %d and counted %d past the first 16; the report:\n%s",
				pipeline, c.launched, c.capped, out)
		}
	}
}

// Every way a run stops at the 32 agent turn ceiling is recorded as one,
// because that is what keeps a resume from trying again and stopping at
// the same place: a repeating stage whose next round does not fit, and a
// pipeline whose first stage does not fit after earlier stages used the
// room.
func TestEveryStopAtTheCeilingIsRecordedAsOne(t *testing.T) {
	twenty := func(p string) (string, map[string]any) {
		if strings.Contains(p, "find things") {
			var out []any
			for i := 1; i <= 20; i++ {
				out = append(out, fmt.Sprintf("f%d", i))
			}
			return "", map[string]any{"found": out}
		}
		return "ok", map[string]any{"done": false, "more": []any{"m"}}
	}
	for name, plan := range map[string]string{
		"repeat": `{"goal":"g","stages":[
		  {"name":"find","kind":"step","agent":"oracle","prompt":"find things","returns":{"found":"strings"}},
		  {"name":"check","kind":"fanout","agent":"oracle","over":["$find.found"],"copies":2,"prompt":"check {{item}}"},
		  {"name":"again","kind":"fanout","agent":"oracle","over":["x"],"prompt":"again {{item}}","returns":{"done":"bool"},"repeat_until":"done","max_rounds":3}]}`,
		"pipeline head": `{"goal":"g","stages":[
		  {"name":"find","kind":"step","agent":"oracle","prompt":"find things","returns":{"found":"strings"}},
		  {"name":"check","kind":"fanout","agent":"oracle","over":["$find.found"],"copies":2,"prompt":"check {{item}}"},
		  {"name":"head","kind":"fanout","agent":"oracle","over":["x","y"],"prompt":"head {{item}}","returns":{"more":"strings"}},
		  {"name":"tail","kind":"fanout","agent":"oracle","over":["$head.more"],"pipeline":true,"prompt":"tail {{item}}"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			m := &scriptedModel{reply: twenty}
			loop := orchestrateLoop(t, m.server(t).URL)
			r := runPlanJSON(t, loop, plan)
			if r.stopped == "" || !r.atCeiling {
				t.Errorf("stopped=%q atCeiling=%v, want a stop recorded as at the ceiling", r.stopped, r.atCeiling)
			}
		})
	}
}
