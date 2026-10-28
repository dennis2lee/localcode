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
