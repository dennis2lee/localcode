package agent

import (
	"strings"
	"sync/atomic"
	"testing"
)

// repeat_until_dry: run a stage again until it stops finding anything new.
//
// The other stopping rule. repeat_until stops when the agent says it is
// done; this one stops when the rounds stop turning anything up, which is
// the rule a search for bugs or flaky tests actually wants and the one a
// model told "keep going until nothing new appears" abandons by the third
// round.

func TestRepeatUntilDryIsValidatedLikeALoop(t *testing.T) {
	for _, tc := range []struct {
		name string
		plan string
		want string
	}{
		{"no max_rounds", `{"goal":"g","stages":[
			{"name":"a","kind":"step","agent":"oracle","prompt":"x","returns":{"found":"strings"},"repeat_until_dry":"found"}]}`,
			"max_rounds of at least 2"},
		{"not a strings field", `{"goal":"g","stages":[
			{"name":"a","kind":"step","agent":"oracle","prompt":"x","returns":{"found":"string"},"repeat_until_dry":"found","max_rounds":3}]}`,
			"strings field"},
		{"unknown field", `{"goal":"g","stages":[
			{"name":"a","kind":"step","agent":"oracle","prompt":"x","returns":{"found":"strings"},"repeat_until_dry":"other","max_rounds":3}]}`,
			`repeat_until_dry names "other"`},
		{"both stopping rules", `{"goal":"g","stages":[
			{"name":"a","kind":"step","agent":"oracle","prompt":"x","returns":{"found":"strings","done":"bool"},
			 "repeat_until":"done","repeat_until_dry":"found","max_rounds":3}]}`,
			"one stopping rule"},
		{"on a barrier", `{"goal":"g","stages":[
			{"name":"a","kind":"barrier","agent":"oracle","prompt":"x","returns":{"found":"strings"},"repeat_until_dry":"found","max_rounds":3}]}`,
			"this stage is a barrier"},
		{"dry_rounds out of range", `{"goal":"g","stages":[
			{"name":"a","kind":"step","agent":"oracle","prompt":"x","returns":{"found":"strings"},"repeat_until_dry":"found","dry_rounds":4,"max_rounds":6}]}`,
			"dry_rounds is 4"},
		{"max_rounds that can never run dry", `{"goal":"g","stages":[
			{"name":"a","kind":"step","agent":"oracle","prompt":"x","returns":{"found":"strings"},"repeat_until_dry":"found","dry_rounds":2,"max_rounds":2}]}`,
			"more than dry_rounds"},
		{"dry_rounds alone", `{"goal":"g","stages":[
			{"name":"a","kind":"step","agent":"oracle","prompt":"x","returns":{"found":"strings"},"dry_rounds":2}]}`,
			"without repeat_until_dry"},
		{"max_rounds alone", `{"goal":"g","stages":[
			{"name":"a","kind":"step","agent":"oracle","prompt":"x","max_rounds":3}]}`,
			"repeats nothing"},
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

	// A bounded dry loop validates, and is priced at every round.
	p := planOf(t, `{"goal":"g","stages":[
		{"name":"a","kind":"fanout","agent":"oracle","over":["x","y"],"prompt":"{{item}}",
		 "returns":{"found":"strings"},"repeat_until_dry":"found","max_rounds":4}]}`)
	if err := p.Validate(roster("oracle")); err != nil {
		t.Fatalf("a bounded dry loop was refused: %v", err)
	}
	if got := p.Launches(); got != 8 {
		t.Errorf("Launches() = %d, want 8 (two items times four rounds)", got)
	}
}

// Rounds keep running while they find something new, stop once one finds
// nothing, and everything every round found reaches the stages after it.
func TestADryLoopStopsWhenARoundFindsNothingNew(t *testing.T) {
	var hunts int32
	m := &scriptedModel{}
	m.reply = func(prompt string) (string, map[string]any) {
		if strings.Contains(prompt, "confirm") {
			return "", map[string]any{"real": true}
		}
		switch atomic.AddInt32(&hunts, 1) {
		case 1:
			return "", map[string]any{"found": []string{"bug a", "bug b"}}
		case 2:
			return "", map[string]any{"found": []string{"Bug  B", "bug c"}}
		default:
			return "", map[string]any{"found": []string{"bug a"}}
		}
	}
	loop := orchestrateLoop(t, m.server(t).URL)
	report := runPlanJSON(t, loop, `{"goal":"g","stages":[
	  {"name":"hunt","kind":"step","agent":"oracle","prompt":"hunt for bugs",
	   "returns":{"found":"strings"},"repeat_until_dry":"found","max_rounds":6},
	  {"name":"check","kind":"fanout","agent":"oracle","over":["$hunt.found"],
	   "prompt":"confirm {{item}}","returns":{"real":"bool"},"keep":"real"}]}`)

	if report.stopped != "" {
		t.Fatalf("the run did not finish: %s", report.stopped)
	}
	h := report.stages[0]
	if h.rounds != 3 {
		t.Errorf("rounds = %d, want 3: two rounds that found something, then one that did not", h.rounds)
	}
	if !h.ranDry {
		t.Error("the stage is not marked as having run dry")
	}
	// Every round's findings, merged: a, b and c, with "Bug  B" the same
	// item as "bug b".
	if c := report.stages[1]; c.launched != 3 {
		t.Errorf("the stage after it checked %d items, want 3 (every round's findings, merged)", c.launched)
	}
	if out := report.String(); !strings.Contains(out, "ran dry after 3 round(s)") {
		t.Errorf("the report does not say the loop ran dry:\n%s", out)
	}

	// The third round was shown everything found so far, not only the
	// round before it.
	var third string
	n := 0
	for _, p := range m.seen() {
		if strings.Contains(p, "hunt for bugs") {
			n++
			if n == 3 {
				third = p
			}
		}
	}
	for _, want := range []string{"bug a", "bug c"} {
		if !strings.Contains(third, want) {
			t.Errorf("the third round was not shown %q from the earlier rounds:\n%s", want, third)
		}
	}
}

// A round in which nobody answered found nothing because nobody looked:
// it does not count as dry.
func TestARoundWithNoAnswersIsNotDry(t *testing.T) {
	m := &scriptedModel{reply: func(string) (string, map[string]any) {
		return "I could not decide", nil
	}}
	loop := orchestrateLoop(t, m.server(t).URL)
	report := runPlanJSON(t, loop, `{"goal":"g","stages":[
	  {"name":"hunt","kind":"step","agent":"oracle","prompt":"hunt",
	   "returns":{"found":"strings"},"repeat_until_dry":"found","max_rounds":3}]}`)

	h := report.stages[0]
	if h.ranDry {
		t.Error("rounds with no answers were taken as having found nothing")
	}
	if h.rounds != 3 {
		t.Errorf("rounds = %d, want 3: the loop must use its rounds when nobody answers", h.rounds)
	}
	if out := report.String(); !strings.Contains(out, "3 round(s) without running dry") {
		t.Errorf("the report does not admit the loop never ran dry:\n%s", out)
	}
}

// dry_rounds asks for several empty rounds in a row, and a new finding
// starts the count again.
func TestDryRoundsCountsConsecutiveEmptyRounds(t *testing.T) {
	var hunts int32
	m := &scriptedModel{reply: func(string) (string, map[string]any) {
		switch atomic.AddInt32(&hunts, 1) {
		case 1:
			return "", map[string]any{"found": []string{"a"}}
		case 2:
			return "", map[string]any{"found": []string{"a"}} // nothing new: 1
		case 3:
			return "", map[string]any{"found": []string{"b"}} // new: back to 0
		default:
			return "", map[string]any{"found": []string{"a", "b"}} // nothing new
		}
	}}
	loop := orchestrateLoop(t, m.server(t).URL)
	report := runPlanJSON(t, loop, `{"goal":"g","stages":[
	  {"name":"hunt","kind":"step","agent":"oracle","prompt":"hunt",
	   "returns":{"found":"strings"},"repeat_until_dry":"found","dry_rounds":2,"max_rounds":8}]}`)

	if h := report.stages[0]; h.rounds != 5 || !h.ranDry {
		t.Errorf("rounds = %d, ran dry = %v; want 5 and true: two empty rounds in a row after the last new finding", h.rounds, h.ranDry)
	}
}

// "New" means not returned before, kept or not: a value a keep filter
// dropped in round 1 is not new when round 2 returns it again, so the loop
// runs dry at round 2.
func TestADryLoopCountsReturnedValuesNotOnlyKeptOnes(t *testing.T) {
	var calls int32
	m := &scriptedModel{reply: func(string) (string, map[string]any) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return "", map[string]any{"found": []any{"bug a"}, "good": false}
		}
		return "", map[string]any{"found": []any{"bug a"}, "good": true}
	}}
	loop := orchestrateLoop(t, m.server(t).URL)
	report := runPlanJSON(t, loop, `{"goal":"g","stages":[
	  {"name":"hunt","kind":"step","agent":"oracle","prompt":"hunt",
	   "returns":{"found":"strings","good":"bool"},"keep":"good",
	   "repeat_until_dry":"found","max_rounds":4}]}`)
	if report.stopped != "" {
		t.Fatalf("did not finish: %s", report.stopped)
	}
	if h := report.stages[0]; h.rounds != 2 || !h.ranDry {
		t.Errorf("rounds=%d ranDry=%v, want 2 and true: round 2 returned nothing not returned before", h.rounds, h.ranDry)
	}
}
