package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// The validator is the whole argument for a plan being data.
//
// A script cannot be refused before it runs; a plan can, and every one of
// these is a run that never starts and a message the model can act on. The
// errors are asserted for what they say, not only that they happened, for
// the same reason the edit tool's failure diagnosis is: the reader is a
// model that will be asked to fix it, and "invalid plan" produces another
// invalid plan.

func roster(names ...string) Limits { return Limits{Agents: names} }

func planOf(t *testing.T, body string) Plan {
	t.Helper()
	var p Plan
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("test plan does not parse: %v", err)
	}
	return p
}

func TestAWorkablePlanValidates(t *testing.T) {
	p := planOf(t, `{
	  "goal":"find real problems",
	  "stages":[
	    {"name":"find","kind":"fanout","agent":"oracle","over":["a","b"],
	     "prompt":"look for {{item}}","returns":{"findings":"strings"}},
	    {"name":"kill","kind":"fanout","agent":"oracle","copies":2,"over":["$find.findings"],
	     "prompt":"refute {{item}}","returns":{"survives":"bool"},"keep":"survives"},
	    {"name":"report","kind":"barrier","agent":"plan","prompt":"write up {{input}}"}
	  ]}`)
	if err := p.Validate(roster("oracle", "plan")); err != nil {
		t.Fatalf("a workable plan was refused: %v", err)
	}
}

func TestAPlanIsRefusedWithTheReason(t *testing.T) {
	cases := []struct {
		name string
		plan string
		want string
	}{
		{"no goal", `{"goal":"  ","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"x"}]}`,
			"no goal"},
		{"no stages", `{"goal":"g","stages":[]}`, "no stages"},
		{"unknown agent", `{"goal":"g","stages":[{"name":"a","kind":"step","agent":"nobody","prompt":"x"}]}`,
			`no agent "nobody"`},
		{"unknown kind", `{"goal":"g","stages":[{"name":"a","kind":"loop","agent":"oracle","prompt":"x"}]}`,
			"must be one of step, fanout, barrier"},
		{"duplicate name", `{"goal":"g","stages":[
			{"name":"a","kind":"step","agent":"oracle","prompt":"x"},
			{"name":"a","kind":"step","agent":"oracle","prompt":"y"}]}`,
			"two stages have that name"},
		{"forward reference", `{"goal":"g","stages":[
			{"name":"a","kind":"fanout","agent":"oracle","over":["$later.x"],"prompt":"{{item}}"},
			{"name":"later","kind":"step","agent":"oracle","prompt":"y","returns":{"x":"strings"}}]}`,
			"does not run before this one"},
		{"reference to an undeclared field", `{"goal":"g","stages":[
			{"name":"a","kind":"step","agent":"oracle","prompt":"x","returns":{"findings":"strings"}},
			{"name":"b","kind":"fanout","agent":"oracle","over":["$a.other"],"prompt":"{{item}}"}]}`,
			`does not return a field called "other"`},
		{"reference to a non-list field", `{"goal":"g","stages":[
			{"name":"a","kind":"step","agent":"oracle","prompt":"x","returns":{"ok":"bool"}},
			{"name":"b","kind":"fanout","agent":"oracle","over":["$a.ok"],"prompt":"{{item}}"}]}`,
			"can only spread over a strings field"},
		{"keep names nothing returned", `{"goal":"g","stages":[
			{"name":"a","kind":"step","agent":"oracle","prompt":"x","returns":{"ok":"bool"},"keep":"other"}]}`,
			`keep names "other"`},
		{"unknown substitution", `{"goal":"g","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"do {{everything}}"}]}`,
			"is not a substitution"},
		{"item outside a fanout", `{"goal":"g","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"do {{item}}"}]}`,
			"only meaningful in a fanout"},
		{"over on a step", `{"goal":"g","stages":[{"name":"a","kind":"step","agent":"oracle","over":["x"],"prompt":"y"}]}`,
			"over belongs to a fanout"},
		{"fanout with no items", `{"goal":"g","stages":[{"name":"a","kind":"fanout","agent":"oracle","prompt":"{{item}}"}]}`,
			"a fanout needs over"},
		{"bad role", `{"goal":"g","stages":[{"name":"a","kind":"step","agent":"oracle","role":"admin","prompt":"x"}]}`,
			"must be one of readonly, builder, runner"},
		{"bad return type", `{"goal":"g","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"x","returns":{"n":"date"}}]}`,
			"the types are string, bool, number, strings"},
		{"bad unanswered", `{"goal":"g","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"x","unanswered":"maybe"}]}`,
			"must be one of skip, keep, fail"},
		{"no prompt", `{"goal":"g","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":" "}]}`,
			"no prompt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := planOf(t, tc.plan).Validate(roster("oracle", "plan"))
			if err == nil {
				t.Fatalf("the plan was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

// Every ceiling is a refusal before anything runs rather than a truncation
// while it does. A run that quietly did two thirds of what it said is the
// failure this whole design is against.
func TestTheCeilingsRefuseRatherThanTruncate(t *testing.T) {
	wide := `{"goal":"g","stages":[{"name":"a","kind":"fanout","agent":"oracle","copies":8,
		"over":["1","2","3","4","5","6","7","8"],"prompt":"{{item}}"}]}`
	err := planOf(t, wide).Validate(roster("oracle"))
	if err == nil || !strings.Contains(err.Error(), "would launch up to 64 agents") {
		t.Errorf("a 64-agent plan was not refused: %v", err)
	}

	// A reference fanout is the one width nobody can know in advance, so
	// validation asks only whether it can run at all and the runner caps
	// the real width against what is left. Pricing it at the ceiling here
	// would refuse the plan this feature exists for.
	ref := `{"goal":"g","stages":[
		{"name":"a","kind":"step","agent":"oracle","prompt":"x","returns":{"f":"strings"}},
		{"name":"b","kind":"fanout","agent":"oracle","copies":4,"over":["$a.f"],"prompt":"{{item}}"}]}`
	if err := planOf(t, ref).Validate(roster("oracle")); err != nil {
		t.Errorf("a reference fanout was refused for a width nobody could know yet: %v", err)
	}
}

func TestLaunchesIsTheWorstCase(t *testing.T) {
	p := planOf(t, `{"goal":"g","stages":[
		{"name":"a","kind":"fanout","agent":"oracle","over":["1","2","3"],"copies":2,"prompt":"{{item}}"},
		{"name":"b","kind":"barrier","agent":"oracle","prompt":"x"}]}`)
	if got := p.Launches(); got != 7 {
		t.Errorf("Launches() = %d, want 7 (3 items x 2 copies, plus one barrier)", got)
	}
}

// The Answer tool's schema is the stage's declaration, rendered.
func TestAStageRendersItsOwnAnswerSchema(t *testing.T) {
	s := Stage{Returns: map[string]string{"survives": "bool", "why": "string", "refs": "strings", "n": "number"}}
	var got struct {
		Properties map[string]struct {
			Type  string          `json:"type"`
			Items json.RawMessage `json:"items"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(s.answerSchema(), &got); err != nil {
		t.Fatalf("the rendered schema does not parse: %v\n%s", err, s.answerSchema())
	}
	want := map[string]string{"survives": "boolean", "why": "string", "refs": "array", "n": "number"}
	for f, typ := range want {
		if got.Properties[f].Type != typ {
			t.Errorf("%s is %q, want %q", f, got.Properties[f].Type, typ)
		}
	}
	if got.Properties["refs"].Items == nil {
		t.Error("a strings field was rendered without items")
	}
	if len(got.Required) != 4 {
		t.Errorf("required has %d fields, want 4: a field the plan asked for is not optional", len(got.Required))
	}
	if (Stage{}).answerSchema() != nil {
		t.Error("a stage that declared nothing still rendered a schema")
	}
}

// The one substitution rule, applied.
func TestAStagePromptSubstitutesExactlyThreeThings(t *testing.T) {
	p := Plan{Goal: "the goal"}
	s := Stage{Kind: "fanout", Prompt: "task={{task}} item={{item}} input={{input}}"}
	got := stagePrompt(p, s, "an item", "carried")
	if got != "task=the goal item=an item input=carried" {
		t.Errorf("substitution = %q", got)
	}

	// A prompt that never mentions the goal still carries it, because a
	// sub-agent cannot see the conversation and a plan author who forgot
	// that produced an agent working blind.
	bare := stagePrompt(p, Stage{Kind: "step", Prompt: "do the thing"}, "", "")
	if !strings.Contains(bare, "the goal") || !strings.Contains(bare, "do the thing") {
		t.Errorf("a prompt without {{task}} lost the goal: %q", bare)
	}
}

func TestKeepDropsWhatIsFalseOrEmpty(t *testing.T) {
	for _, tc := range []struct {
		v    any
		want bool
	}{
		{true, true}, {false, false},
		{"something", true}, {"  ", false}, {"", false},
		{float64(1), true}, {float64(0), false},
		{[]any{"a"}, true}, {[]any{}, false},
		{nil, false},
	} {
		if got := truthy(tc.v); got != tc.want {
			t.Errorf("truthy(%#v) = %v, want %v", tc.v, got, tc.want)
		}
	}
}
