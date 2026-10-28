package agent

// Red-team round 2, lens: test honesty and what the docs promise.
// Each test below states the docs/USAGE.md line it holds the code to.

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// The Orchestrate result names its run, so a run stopped by failed units,
// the case a resume exists for, can be resumed by its id.
func TestTheRunIDIsInTheReplyThatReportedIt(t *testing.T) {
	m := &scriptedModel{reply: func(p string) (string, map[string]any) {
		if i := strings.LastIndex(p, "look at "); i >= 0 {
			item := p[i+len("look at "):]
			return "looked at " + item, map[string]any{"seen": item}
		}
		return "summed", nil
	}}
	fail := &sync.Map{}
	fail.Store("look at d", true)
	loop := orchestrateLoop(t, failingModel(t, m, fail))
	res := executeOrchestrate(context.Background(), loop, lookThenSum)
	run := runIDs(loop, "main")[0]
	if !strings.Contains(res.Content, run) {
		t.Errorf("the Orchestrate result for a run with a failed unit does not name run %s:\n%s", run, res.Content)
	}
}
