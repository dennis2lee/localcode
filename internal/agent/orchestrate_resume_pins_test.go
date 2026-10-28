package agent

// Red-team round 2, lens: test honesty. These pass at 0d2c95d. Each one
// pins a documented behaviour that no test added in v0.169.0 pins: the
// named mutant of the production code survives every test in the package
// and is killed by the test here.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/tools"
)

// Two copies of one item have the same prompt; each keeps its own answer
// through a resume.
func TestCopiesKeepTheirOwnAnswersThroughAResume(t *testing.T) {
	var n atomic.Int32
	m := &scriptedModel{reply: func(p string) (string, map[string]any) {
		if strings.Contains(p, "sum up") {
			return "summed", nil
		}
		k := n.Add(1)
		return fmt.Sprintf("verdict-%d", k), map[string]any{"verdict": fmt.Sprintf("verdict-%d", k)}
	}}
	fail := &sync.Map{}
	fail.Store("sum up", true)
	loop := orchestrateLoop(t, failingModel(t, m, fail))
	plan := `{"goal":"g","stages":[
	  {"name":"judge","kind":"fanout","agent":"oracle","over":["x"],"copies":2,"prompt":"judge {{item}}","returns":{"verdict":"string"}},
	  {"name":"sum","kind":"step","agent":"oracle","prompt":"sum up {{input}}"}]}`
	executeOrchestrate(context.Background(), loop, plan)
	fail.Delete("sum up")
	out := resume(t, loop, "main", "")
	for _, want := range []string{"verdict-1", "verdict-2"} {
		if !strings.Contains(out, want) {
			t.Errorf("the resumed report lost %s, one copy's own answer:\n%s", want, out)
		}
	}
}

// A run with a runner stage asks before it resumes, as one with a builder
// stage does.
func TestResumingARunnerRunAsks(t *testing.T) {
	loop, _, _, asks := workflowLoop(t, func(string) (string, map[string]any) { return "ok", nil })
	plan := `{"goal":"run it","stages":[{"name":"build","kind":"step","agent":"oracle","role":"runner","prompt":"build"}]}`
	if res := executeOrchestrateIn(loop, "conv", plan); res.IsError {
		t.Fatalf("the run failed: %s", res.Content)
	}
	asks.allow = false
	if out := resume(t, loop, "conv", ""); !strings.HasPrefix(out, "Not resumed") {
		t.Errorf("a runner run resumed without asking:\n%s", out)
	}
	if len(asks.asks) != 1 || !strings.Contains(asks.asks[0].Description, "build can change the workspace") {
		t.Errorf("asked %+v, want one question naming the runner stage", asks.asks)
	}
}

// A resume is refused where /workflow run is: in a scheduled turn and from
// a command the model booked.
func TestResumeIsRefusedWhereNobodyTypedIt(t *testing.T) {
	m := &scriptedModel{reply: func(string) (string, map[string]any) { return "ok", nil }}
	loop := orchestrateLoop(t, m.server(t).URL)
	executeOrchestrate(context.Background(), loop, `{"goal":"g","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"x"}]}`)
	for name, ctx := range map[string]context.Context{
		"unattended":     WithUnattended(context.Background()),
		"booked command": withCommandRun(context.Background()),
	} {
		loop.SendMessage(ctx, "main", "oracle", "/workflow resume")
		if out := lastAssistantText(loop.Store, "main"); !strings.HasPrefix(out, "Not resumed") {
			t.Errorf("%s: a resume was not refused:\n%s", name, out)
		}
	}
	if ids := runIDs(loop, "main"); len(ids) != 1 {
		t.Errorf("a refused resume started a run: %v", ids)
	}
}

// A resume of a /workflow run with arguments gives the rerun units the
// arguments, and a resume of that resume still has them.
func TestAResumedWorkflowRunKeepsItsArguments(t *testing.T) {
	var failSum atomic.Bool
	failSum.Store(true)
	var mu sync.Mutex
	var sums []string
	loop, _, home, _ := workflowLoop(t, func(p string) (string, map[string]any) {
		if strings.Contains(p, "sum for") {
			mu.Lock()
			sums = append(sums, p)
			mu.Unlock()
			if failSum.Load() {
				return "cannot say", nil // no Answer: unanswered, and the stage says fail
			}
			return "", map[string]any{"total": "done"}
		}
		return "looked", map[string]any{"seen": "yes"}
	})
	writeWorkflow(t, home, "rev", `{"name":"rev","args":"paths","plan":{"goal":"review {{args}}","stages":[
	  {"name":"look","kind":"fanout","agent":"oracle","over":["$args"],"prompt":"look at {{item}}","returns":{"seen":"string"}},
	  {"name":"sum","kind":"step","agent":"oracle","prompt":"sum for {{args}}: {{input}}","returns":{"total":"string"},"unanswered":"fail"}]}}`)
	send := func(line string) string {
		t.Helper()
		if err := loop.SendMessage(context.Background(), "conv", "oracle", line); err != nil {
			t.Fatal(err)
		}
		return lastAssistantText(loop.Store, "conv")
	}
	send("/workflow run rev src/a.go src/b.go")
	failSum.Store(false)
	out := send("/workflow resume")
	if !strings.Contains(out, "Resumed run") || !strings.Contains(out, "2 of them answers reused") {
		t.Errorf("a resume of a workflow run with arguments did not run, or reused nothing:\n%s", out)
	}
	mu.Lock()
	last := ""
	if len(sums) > 0 {
		last = sums[len(sums)-1]
	}
	mu.Unlock()
	if !strings.Contains(last, "sum for src/a.go src/b.go") {
		t.Errorf("the resumed sum unit was not given the arguments; its prompt:\n%s", last)
	}
	out = send("/workflow resume")
	if !strings.Contains(out, "3 of them answers reused") {
		t.Errorf("a resume of the resume did not reuse every unit:\n%s", out)
	}
}

// A free slot goes to the earlier stage of a pipeline first, so the
// stages that find things are not starved by the ones checking them.
func TestAPipelineGivesAFreeSlotToTheEarlierStage(t *testing.T) {
	m := &scriptedModel{reply: func(p string) (string, map[string]any) {
		if i := strings.LastIndex(p, "find in "); i >= 0 {
			// Unit k finishes at k*60ms, so each slot frees on its own.
			item := p[i+len("find in "):]
			time.Sleep(time.Duration(item[0]-'0') * 60 * time.Millisecond)
			return "", map[string]any{"found": []string{"from " + item}}
		}
		return "checked", nil
	}}
	loop := orchestrateLoop(t, m.server(t).URL)
	runPlanJSON(t, loop, `{"goal":"g","stages":[
	  {"name":"find","kind":"fanout","agent":"oracle","over":["1","2","3","4","5","6"],"prompt":"find in {{item}}","returns":{"found":"strings"}},
	  {"name":"check","kind":"fanout","agent":"oracle","over":["$find.found"],"pipeline":true,"prompt":"check {{item}}"}]}`)
	evs, _ := loop.Store.Events("main", 0)
	finds := 0
	for _, e := range evs {
		if e.Type != events.TypeTaskSpawned {
			continue
		}
		if e.Data["stage"] == "check" {
			break
		}
		if e.Data["stage"] == "find" {
			finds++
		}
	}
	if finds != 6 {
		t.Errorf("a check unit took a slot while %d of the 6 find units had not started", 6-finds)
	}
}

// A pipelined stage takes at most 16 items, as any fanout over an earlier
// stage's results does.
func TestAPipelinedStageTakesAtMostSixteenItems(t *testing.T) {
	m := &scriptedModel{reply: func(p string) (string, map[string]any) {
		if strings.Contains(p, "find in") {
			var out []string
			for i := 1; i <= 20; i++ {
				out = append(out, fmt.Sprintf("f%d", i))
			}
			return "", map[string]any{"found": out}
		}
		return "checked", nil
	}}
	loop := orchestrateLoop(t, m.server(t).URL)
	report := runPlanJSON(t, loop, `{"goal":"g","stages":[
	  {"name":"find","kind":"fanout","agent":"oracle","over":["x"],"prompt":"find in {{item}}","returns":{"found":"strings"}},
	  {"name":"check","kind":"fanout","agent":"oracle","over":["$find.found"],"pipeline":true,"prompt":"check {{item}}"}]}`)
	if c := report.stages[1]; c.launched != maxFanout {
		t.Errorf("the pipelined stage launched %d units, want the first %d items", c.launched, maxFanout)
	}
}

// A unit whose agent now runs on another model is run again by a resume,
// not reused.
func TestAChangedModelRunsTheUnitsAgain(t *testing.T) {
	m := &scriptedModel{reply: func(p string) (string, map[string]any) {
		if i := strings.LastIndex(p, "look at "); i >= 0 {
			return "looked", map[string]any{"seen": p[i+len("look at "):]}
		}
		return "summed", nil
	}}
	fail := &sync.Map{}
	fail.Store("sum up", true)
	loop := orchestrateLoop(t, failingModel(t, m, fail))
	executeOrchestrate(context.Background(), loop, lookThenSum)
	fail.Delete("sum up")
	loop.Config.Profiles["m2"] = config.Profile{Provider: "p", Model: "another-model"}
	loop.Config.Agents["oracle"] = config.AgentConfig{Profile: "m2"}
	if out := resume(t, loop, "main", ""); strings.Contains(out, "answers reused") {
		t.Errorf("after the agent moved to another model, the resume reused answers the old model gave:\n%s", out)
	}
}

func executeOrchestrateIn(loop *Loop, session, plan string) tools.Result {
	return NewOrchestrateTool(loop).Execute(WithSessionID(loop.pinSmart(context.Background()), session), []byte(plan))
}

// A deny rule for Orchestrate refuses a home workflow and a resume, as it
// does a project workflow.
func TestADenyRuleRefusesAHomeWorkflowAndAResume(t *testing.T) {
	loop, _, home, _ := workflowLoop(t, func(string) (string, map[string]any) { return "ok", nil })
	writeWorkflow(t, home, "mine", workflowBody("mine", "my own"))
	if res := executeOrchestrateIn(loop, "conv", `{"goal":"g","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"x"}]}`); res.IsError {
		t.Fatalf("the run failed: %s", res.Content)
	}
	loop.Tools.Resolver = func(_ context.Context, q tools.Query) tools.Outcome {
		if q.Tool == orchestrateToolName {
			return tools.Outcome{Decision: tools.DecisionDeny}
		}
		return tools.Outcome{Decision: tools.DecisionAllow}
	}
	for _, c := range []struct{ line, want string }{
		{"/workflow run mine", "Not run"},
		{"/workflow resume", "Not resumed"},
	} {
		loop.SendMessage(context.Background(), "conv", "oracle", c.line)
		if out := lastAssistantText(loop.Store, "conv"); !strings.HasPrefix(out, c.want) {
			t.Errorf("%s under a deny rule for Orchestrate:\n%s", c.line, out)
		}
	}
	if ids := runIDs(loop, "conv"); len(ids) != 1 {
		t.Errorf("a denied workflow or resume started a run: %v", ids)
	}
}
