package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"localcode/internal/config"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// A run, end to end.
//
// The point of the whole feature is that the shape is kept by code rather
// than by a model remembering to, so these assert the shape: that every
// item really got its agents, that a keep filter really dropped what it
// said it would, that a stage which did not answer was neither counted as
// surviving nor as killed, and that the report says what happened rather
// than what was intended.

// scriptedModel answers each turn according to what the prompt contains.
// reply returns the assistant text, or a tool call to Answer when the
// second return is non-empty.
type scriptedModel struct {
	mu      sync.Mutex
	prompts []string
	reply   func(prompt string) (text string, answer map[string]any)
	turns   int32
}

func (m *scriptedModel) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&m.turns, 1)
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		json.Unmarshal(raw, &body)
		prompt := ""
		for _, msg := range body.Messages {
			if msg.Role == "user" {
				var s string
				if json.Unmarshal(msg.Content, &s) == nil {
					prompt = s
				}
			}
		}
		// Whether this conversation has already answered. Read off the
		// request rather than remembered, because several stages share one
		// server and each has its own child session.
		answered := strings.Contains(string(raw), `"tool_calls"`)

		m.mu.Lock()
		m.prompts = append(m.prompts, prompt)
		m.mu.Unlock()

		text, answer := m.reply(prompt)
		w.Header().Set("Content-Type", "text/event-stream")
		if answer != nil && !answered {
			args, _ := json.Marshal(answer)
			esc, _ := json.Marshal(string(args))
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"a1\",\"function\":{\"name\":\"Answer\",\"arguments\":%s}}]}}]}\n\n", esc)
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		} else {
			enc, _ := json.Marshal(text)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\n", enc)
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (m *scriptedModel) seen() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.prompts...)
}

func orchestrateLoop(t *testing.T, url string) *Loop {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	reg := tools.NewRegistry(nil)
	reg.Register(tools.ReadFile{})
	reg.Register(NewAnswerTool())
	on := true
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"p": {Type: config.ProviderOpenAICompat, BaseURL: url}},
		Profiles:       map[string]config.Profile{"m": {Provider: "p", Model: "test-model"}},
		Agents:         map[string]config.AgentConfig{"oracle": {Profile: "m"}, "plan": {Profile: "m"}},
		DefaultProfile: "m",
		Orchestrate:    &on,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	loop := New(store, reg, map[string]provider.Provider{"p": provider.NewOpenAICompat(url, "")}, cfg)
	NewTaskManager(context.Background(), loop, 4)
	reg.Register(NewOrchestrateTool(loop))
	if _, err := loop.Store.CreateSession("main", "", "oracle", true); err != nil {
		t.Fatalf("session: %v", err)
	}
	return loop
}

func runPlanJSON(t *testing.T, loop *Loop, body string) runReport {
	t.Helper()
	var p Plan
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := p.Validate(Limits{Agents: []string{"oracle", "plan"}}); err != nil {
		t.Fatalf("the test plan is invalid: %v", err)
	}
	ctx := loop.pinSmart(context.Background())
	return loop.runPlan(ctx, "main", p)
}

// The adversarial pattern, which is the reason the feature exists: find
// things, then try to kill each one with agents that cannot see each other,
// and keep only what survived.
func TestAFanoutOverAnEarlierStageRunsEveryItemAndKeepsWhatSurvives(t *testing.T) {
	m := &scriptedModel{}
	m.reply = func(prompt string) (string, map[string]any) {
		switch {
		case strings.Contains(prompt, "look for"):
			return "", map[string]any{"findings": []string{"bug one", "bug two", "bug three"}}
		case strings.Contains(prompt, "refute"):
			// "bug two" is refuted by everyone; the others survive.
			return "", map[string]any{"survives": !strings.Contains(prompt, "bug two")}
		default:
			return "the write-up", nil
		}
	}
	loop := orchestrateLoop(t, m.server(t).URL)

	report := runPlanJSON(t, loop, `{"goal":"find real problems","stages":[
	  {"name":"find","kind":"step","agent":"oracle","prompt":"look for problems",
	   "returns":{"findings":"strings"}},
	  {"name":"kill","kind":"fanout","agent":"oracle","copies":2,"over":["$find.findings"],
	   "prompt":"refute this: {{item}}","returns":{"survives":"bool"},"keep":"survives"},
	  {"name":"report","kind":"barrier","agent":"plan","prompt":"write up: {{input}}"}
	]}`)

	if report.stopped != "" {
		t.Fatalf("the run did not finish: %s", report.stopped)
	}
	// 1 finder + 3 findings x 2 skeptics + 1 barrier.
	if report.launched != 8 {
		t.Errorf("launched %d agents, want 8", report.launched)
	}
	kill := report.stages[1]
	if kill.launched != 6 {
		t.Errorf("the skeptic stage launched %d, want 6: every finding must get every copy", kill.launched)
	}
	if kill.kept != 4 {
		t.Errorf("the skeptic stage kept %d, want 4: the two copies of the refuted finding must be dropped", kill.kept)
	}

	// The barrier really saw what survived, and not what did not.
	var barrier string
	for _, p := range m.seen() {
		if strings.Contains(p, "write up") {
			barrier = p
		}
	}
	if !strings.Contains(barrier, "kill") {
		t.Errorf("the barrier was not handed the earlier stage's results:\n%s", barrier)
	}
}

// A stage that did not answer in the declared shape is neither a survivor
// nor a casualty. Which it becomes is the plan author's choice, and the
// report has to name it either way.
func TestAnUnansweredStageIsItsOwnOutcome(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		wantKept int
	}{{"skip", 0}, {"keep", 2}} {
		t.Run(tc.mode, func(t *testing.T) {
			m := &scriptedModel{reply: func(string) (string, map[string]any) {
				return "I had a look and it seems fine, probably", nil
			}}
			loop := orchestrateLoop(t, m.server(t).URL)
			report := runPlanJSON(t, loop, fmt.Sprintf(`{"goal":"g","stages":[
			  {"name":"kill","kind":"fanout","agent":"oracle","over":["a","b"],
			   "prompt":"refute {{item}}","returns":{"survives":"bool"},
			   "keep":"survives","unanswered":%q}]}`, tc.mode))

			s := report.stages[0]
			if s.unanswered != 2 {
				t.Errorf("unanswered = %d, want 2", s.unanswered)
			}
			if s.kept != tc.wantKept {
				t.Errorf("kept = %d, want %d under unanswered:%s", s.kept, tc.wantKept, tc.mode)
			}
			if out := report.String(); !strings.Contains(out, "did not answer in the declared shape") {
				t.Errorf("the report does not name the unanswered results:\n%s", out)
			}
		})
	}
}

func TestUnansweredFailStopsTheRun(t *testing.T) {
	m := &scriptedModel{reply: func(string) (string, map[string]any) { return "prose", nil }}
	loop := orchestrateLoop(t, m.server(t).URL)
	report := runPlanJSON(t, loop, `{"goal":"g","stages":[
	  {"name":"a","kind":"step","agent":"oracle","prompt":"x","returns":{"ok":"bool"},"unanswered":"fail"},
	  {"name":"b","kind":"step","agent":"plan","prompt":"never reached"}]}`)
	if !strings.Contains(report.stopped, "unanswered: fail") {
		t.Errorf("the run did not stop: %q", report.stopped)
	}
	if len(report.stages) != 1 {
		t.Errorf("%d stages ran; the second must not have", len(report.stages))
	}
}

// Every stage is a synchronous child, which is what makes Esc reach the
// whole run rather than only the loop driving it.
func TestCancellingTheTurnStopsTheRun(t *testing.T) {
	var started int32
	m := &scriptedModel{reply: func(string) (string, map[string]any) {
		atomic.AddInt32(&started, 1)
		time.Sleep(2 * time.Second)
		return "slow", nil
	}}
	loop := orchestrateLoop(t, m.server(t).URL)

	var p Plan
	json.Unmarshal([]byte(`{"goal":"g","stages":[
	  {"name":"a","kind":"fanout","agent":"oracle","over":["1","2","3","4"],"prompt":"{{item}}"},
	  {"name":"b","kind":"step","agent":"plan","prompt":"after"}]}`), &p)

	ctx, cancel := context.WithCancel(loop.pinSmart(context.Background()))
	go func() { time.Sleep(150 * time.Millisecond); cancel() }()

	began := time.Now()
	report := loop.runPlan(ctx, "main", p)
	elapsed := time.Since(began)

	if elapsed > 1500*time.Millisecond {
		t.Errorf("the run took %v after being cancelled", elapsed)
	}
	if report.stopped == "" && len(report.stages) > 1 {
		t.Error("the run carried on to the next stage after being cancelled")
	}
}

// A plan cannot run a plan, and it cannot run at all with the switch off.
func TestTheToolRefusesForReasonsItNames(t *testing.T) {
	m := &scriptedModel{reply: func(string) (string, map[string]any) { return "ok", nil }}
	loop := orchestrateLoop(t, m.server(t).URL)
	tool := NewOrchestrateTool(loop)
	plan := json.RawMessage(`{"goal":"g","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"x"}]}`)

	on := loop.pinSmart(context.Background())
	if res := tool.Execute(withInOrchestration(on), plan); !res.IsError || !strings.Contains(res.Content, "cannot run a plan") {
		t.Errorf("a nested run was not refused: %q", res.Content)
	}

	loop.SetOrchestrateEnabled(false)
	off := loop.pinSmart(context.Background())
	res := tool.Execute(off, plan)
	if !res.IsError || !strings.Contains(res.Content, "orchestration is off") {
		t.Errorf("the switch was not honoured: %q", res.Content)
	}
	if !res.Refused {
		t.Error("a refusal was not marked as one, so the carry-on nudge would treat it as work still to do")
	}
}

// An invalid plan costs nothing: it is refused with the reason and no agent
// is launched. That is the whole argument for the plan being data.
func TestAnInvalidPlanLaunchesNothing(t *testing.T) {
	m := &scriptedModel{reply: func(string) (string, map[string]any) { return "ok", nil }}
	loop := orchestrateLoop(t, m.server(t).URL)
	tool := NewOrchestrateTool(loop)

	ctx := WithSessionID(loop.pinSmart(context.Background()), "main")
	res := tool.Execute(ctx, json.RawMessage(`{"goal":"g","stages":[
	  {"name":"a","kind":"step","agent":"nobody-at-all","prompt":"x"}]}`))
	if !res.IsError || !strings.Contains(res.Content, "refused before anything ran") {
		t.Errorf("result = %q", res.Content)
	}
	if n := atomic.LoadInt32(&m.turns); n != 0 {
		t.Errorf("%d model turns ran for a plan that was refused", n)
	}
}

// A stage names a role and cannot enumerate tools, and a role can never
// widen what the agent itself is allowed.
func TestARoleCannotWidenAnAgentsOwnRestriction(t *testing.T) {
	m := &scriptedModel{reply: func(string) (string, map[string]any) { return "ok", nil }}
	loop := orchestrateLoop(t, m.server(t).URL)
	loop.Config.Agents["reader"] = config.AgentConfig{Profile: "m", Tools: []string{"read_file"}}

	ctx := loop.pinSmart(context.Background())
	got := loop.toolsForRole(ctx, "reader", "runner")
	for _, name := range got {
		if name == "bash" {
			t.Errorf("the runner role handed bash to an agent restricted to read_file: %v", got)
		}
	}
	if len(got) != 2 || got[0] != "read_file" || got[1] != answerToolName {
		t.Errorf("allowlist = %v, want read_file plus Answer", got)
	}

	// And an unrestricted agent gets the role's set, plus Answer.
	free := loop.toolsForRole(ctx, "oracle", "readonly")
	if len(free) != len(roleTools["readonly"])+1 {
		t.Errorf("readonly allowlist = %v", free)
	}
}

// A fanout over an earlier stage's findings merges repeats.
//
// Found by printing what a run actually produces rather than by reasoning
// about it. A review stage fanned out over two dimensions returns the same
// finding from every dimension that noticed it, so the skeptic stage was
// launching four agents on what were two distinct findings and calling the
// result four findings. Half the run, spent twice.
func TestARepeatedFindingIsOneItem(t *testing.T) {
	m := &scriptedModel{}
	m.reply = func(prompt string) (string, map[string]any) {
		if strings.Contains(prompt, "review for") {
			// Both dimensions notice the same two things, spelled slightly
			// differently the second time.
			if strings.Contains(prompt, "concurrency") {
				return "", map[string]any{"findings": []string{"A  nil deref in foo()", "a race in bar()"}}
			}
			return "", map[string]any{"findings": []string{"a nil deref in foo()", "a race in bar()"}}
		}
		return "", map[string]any{"survives": true}
	}
	loop := orchestrateLoop(t, m.server(t).URL)

	report := runPlanJSON(t, loop, `{"goal":"g","stages":[
	  {"name":"find","kind":"fanout","agent":"oracle","over":["correctness","concurrency"],
	   "prompt":"review for {{item}}","returns":{"findings":"strings"}},
	  {"name":"kill","kind":"fanout","agent":"oracle","copies":2,"over":["$find.findings"],
	   "prompt":"refute {{item}}","returns":{"survives":"bool"},"keep":"survives"}]}`)

	kill := report.stages[1]
	if kill.launched != 4 {
		t.Errorf("the skeptic stage launched %d agents; two distinct findings times two copies is 4", kill.launched)
	}
	if kill.merged != 2 {
		t.Errorf("merged = %d, want 2", kill.merged)
	}
	// Not silent. A merge nobody is told about is the same defect as a cap
	// nobody is told about.
	if out := report.String(); !strings.Contains(out, "2 repeat(s) of an item merged") {
		t.Errorf("the report does not say what it merged:\n%s", out)
	}
}

// The permission prompt may not name a number the runner cannot reach.
func TestThePermissionEstimateNeverExceedsTheRunCeiling(t *testing.T) {
	var p Plan
	json.Unmarshal([]byte(`{"goal":"g","stages":[
	  {"name":"a","kind":"step","agent":"oracle","prompt":"x","returns":{"f":"strings"}},
	  {"name":"b","kind":"fanout","agent":"oracle","copies":2,"over":["$a.f"],"prompt":"{{item}}"},
	  {"name":"c","kind":"barrier","agent":"plan","prompt":"y"}]}`), &p)

	if got := p.Launches(); got > maxRunAgents {
		t.Errorf("Launches() = %d, which is more than the %d the runner allows: the prompt would ask for a yes to a number that cannot happen", got, maxRunAgents)
	}

	m := &scriptedModel{reply: func(string) (string, map[string]any) { return "ok", nil }}
	loop := orchestrateLoop(t, m.server(t).URL)
	body, _ := json.Marshal(p)
	if d := NewOrchestrateTool(loop).Describe(body); !strings.Contains(d, "at most 32 agent turns") {
		t.Errorf("the permission prompt reads %q", d)
	}
}
