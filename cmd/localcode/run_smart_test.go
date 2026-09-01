package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Smart Agent in a one-shot run.
//
// These are all the same assertion from different angles: what the model
// was actually offered, read off the request that went out. The defect
// these were written for was invisible from the config — smart_agent was
// on, the roster resolved, the orchestration prompt was even sent, and the
// tool that prompt describes had never been registered in this process. A
// test of the setting would have passed.

// smartHome is runHome with Smart Agent turned on and a second profile for
// the specialists to route to, so the roster resolves rather than coming
// back empty on a config with nothing to route to.
func smartHome(t *testing.T, modelURL string, extra string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".localcode"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{
	  "providers": {"local": {"type": "openai-compat", "base_url": %q}},
	  "profiles": {
	    "balanced": {"provider": "local", "model": "the-default-model"},
	    "fast": {"provider": "local", "model": "the-fast-model"}
	  },
	  "default_profile": "balanced",
	  "agents": {"general-purpose": {"profile": "balanced"}}%s
	}`, modelURL+"/v1", extra)
	if err := os.WriteFile(filepath.Join(home, ".localcode", "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(home, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Chdir(work)
	return home
}

const smartOn = `, "smart_agent": true`

// The whole of the report: smart_agent was on and the model was handed no
// way to delegate.
func TestSmartAgentGivesAOneShotRunSomewhereToDelegate(t *testing.T) {
	f := &fakeModel{}
	smartHome(t, f.server(t).URL, smartOn)
	if _, err := doRun(t, runOptions{format: formatText, agent: "general-purpose"}, "x"); err != nil {
		t.Fatal(err)
	}
	got := f.sawTools()
	if !contains(got, "Task") {
		t.Errorf("smart_agent is on and the run offered no Task tool.\noffered: %v", got)
	}
}

// The prompt that tells the model to delegate and the tool it delegates
// with have to arrive together. A prompt describing a tool the model was
// not given is worse than neither: it spends the turn narrating a
// delegation that cannot happen.
func TestTheOrchestrationPromptAndTheToolArriveTogether(t *testing.T) {
	f := &fakeModel{}
	smartHome(t, f.server(t).URL, smartOn)
	if _, err := doRun(t, runOptions{format: formatText, agent: "general-purpose"}, "x"); err != nil {
		t.Fatal(err)
	}
	saidSo := strings.Contains(f.sawSystem(), "Task")
	hasIt := contains(f.sawTools(), "Task")
	if saidSo != hasIt {
		t.Errorf("the prompt talks about delegating = %v, but the Task tool was offered = %v", saidSo, hasIt)
	}
	if !saidSo {
		t.Error("smart_agent is on and nothing in the prompt tells the model it can delegate")
	}
}

// Off is still off. The roster is the whole reason there is anyone to
// delegate to, so without it a single-agent config has nowhere to send
// work and Task would be an expensive way for a model to call itself.
func TestWithoutSmartAgentAOneShotStillHasNowhereToDelegate(t *testing.T) {
	f := &fakeModel{}
	smartHome(t, f.server(t).URL, "")
	if _, err := doRun(t, runOptions{format: formatText, agent: "general-purpose"}, "x"); err != nil {
		t.Fatal(err)
	}
	if got := f.sawTools(); contains(got, "Task") {
		t.Errorf("a single-agent config with smart_agent off was offered Task anyway: %v", got)
	}
}

// Parallel delegation is the half a harness measures, and it is also the
// half the orchestration prompt names by tool. Step 3 of that prompt says
// to launch with TaskBackground and pick up with TaskCollect; a run that
// sends the step and withholds the tools is the same defect as above,
// one paragraph further down.
func TestAOneShotCanDelegateInParallel(t *testing.T) {
	f := &fakeModel{}
	smartHome(t, f.server(t).URL, smartOn)
	if _, err := doRun(t, runOptions{format: formatText, agent: "general-purpose"}, "x"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"TaskBackground", "TaskCollect"} {
		if !contains(f.sawTools(), name) {
			t.Errorf("the prompt tells the model to use %q and the run did not offer it.\noffered: %v", name, f.sawTools())
		}
	}
}

// What is left out, and why. Each of these would be a tool that can only
// refuse, which is a turn the model spends discovering it: booking work
// needs something to still be here when the time comes, reading another
// conversation needs another conversation, and a debate can only be
// started from a conversation somebody is having — every turn in a pipe
// is unattended, which is the first thing debateRefusal checks.
func TestAOneShotLeavesOutWhatItCannotHonour(t *testing.T) {
	f := &fakeModel{}
	smartHome(t, f.server(t).URL, smartOn)
	if _, err := doRun(t, runOptions{format: formatText, agent: "general-purpose"}, "x"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Schedule", "session_read", "Debate"} {
		if contains(f.sawTools(), name) {
			t.Errorf("a one-shot run offered %q, which it can only refuse", name)
		}
	}
}

// delegatingModel launches one background sub-agent and then answers
// without collecting it, which is the case the run has to survive: the
// model was told the work was under way, and the process exiting is what
// would stop it.
//
// The two roles are told apart by the system prompt rather than by call
// order, because they overlap — the orchestrator's next turn and the
// sub-agent's first one are in flight at the same time, which is the
// whole point of a background launch.
type delegatingModel struct {
	mu          sync.Mutex
	parentTurns int
	childRan    bool
}

func (d *delegatingModel) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		blob, _ := json.Marshal(body.Messages)

		w.Header().Set("Content-Type", "text/event-stream")
		defer w.(http.Flusher).Flush()

		if strings.Contains(string(blob), "You are the explore agent") {
			// Slower than the orchestrator's remaining turn, so a run that
			// does not wait finishes first and this never gets recorded.
			time.Sleep(300 * time.Millisecond)
			d.mu.Lock()
			d.childRan = true
			d.mu.Unlock()
			fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"content":"found it"}}]}`+"\n\n")
			fmt.Fprint(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}

		d.mu.Lock()
		d.parentTurns++
		first := d.parentTurns == 1
		d.mu.Unlock()
		if first {
			fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"TaskBackground","arguments":"{\"agent\":\"explore\",\"prompt\":\"find the thing\"}"}}]}}]}`+"\n\n")
			fmt.Fprint(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"content":"the answer"}}]}`+"\n\n")
		fmt.Fprint(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A background sub-agent outlives the turn that launched it, by design.
// In a daemon there is a process for it to outlive into; here the only one
// is this, so the run waits rather than killing work it has already told
// the model was under way.
func TestARunWaitsForTheSubAgentsItLaunched(t *testing.T) {
	d := &delegatingModel{}
	smartHome(t, d.server(t).URL, smartOn)

	if _, err := doRun(t, runOptions{format: formatText, agent: "general-purpose"}, "x"); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	turns, ran := d.parentTurns, d.childRan
	d.mu.Unlock()
	if turns < 2 {
		t.Fatalf("the orchestrator took %d turns; the TaskBackground call never happened, so this proves nothing", turns)
	}
	if !ran {
		t.Error("the run returned while a background sub-agent it launched was still working")
	}
}
