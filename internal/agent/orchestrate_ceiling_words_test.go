package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// A run that did not stop at the ceiling, with a failed unit and a last
// answer of several lines ending in a ceiling stop line, is resumed.
func TestARunWithAFailedUnitAndAQuotedStopIsResumed(t *testing.T) {
	const quotedStop = "The run did not finish: stopped before x: the run had launched 31 agents and this stage needs 2 more, past the limit of 32"

	m := &scriptedModel{reply: func(p string) (string, map[string]any) {
		if strings.HasSuffix(p, "inspect b") {
			return "notes on ceiling wording:\n" + quotedStop, nil
		}
		return "inspected a", nil
	}}
	fail := &sync.Map{}
	fail.Store("inspect a", true)
	loop := orchestrateLoop(t, failingModel(t, m, fail))

	res := executeOrchestrate(context.Background(), loop, `{"goal":"g","stages":[
	  {"name":"scan","kind":"fanout","agent":"oracle","over":["a","b"],"prompt":"inspect {{item}}"}]}`)
	if res.IsError {
		t.Fatalf("unexpected orchestrate error: %s", res.Content)
	}

	fail.Delete("inspect a")
	out := resume(t, loop, "main", "")
	if strings.Contains(out, "Not resumed") && strings.Contains(out, "ceiling") {
		t.Fatalf("2-agent run with multiline output in its last unit was falsely refused as stopped at the 32-agent ceiling:\n%s", out)
	}
	if !strings.Contains(out, "inspected a") {
		t.Fatalf("expected resumed run to finish unit a, got:\n%s", out)
	}
}

// A stage name has no line break or other control character in it. A
// name goes into the report's lines, and one with a line break split the
// stop line a resume reads to tell a run stopped at the ceiling.
func TestAStageNameWithALineBreakIsRefused(t *testing.T) {
	for _, name := range []string{"last\nstage", "last\rstage", "last\u0085stage", "last\x00stage"} {
		p := Plan{Goal: "g", Stages: []Stage{{Name: name, Kind: "step", Agent: "oracle", Prompt: "p"}}}
		if err := p.Validate(roster("oracle")); err == nil || !strings.Contains(err.Error(), "line breaks") {
			t.Errorf("stage name %q: Validate = %v, want it refused", name, err)
		}
	}
}
