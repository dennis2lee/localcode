package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Orchestration, end to end, through the pipe.
//
// internal/agent already drives runPlan directly and asserts the shape a
// run keeps. What nothing covered is the entrance: a model calling the
// tool inside a real turn, in this mode, with this mode's permission
// answer. That gap is where both of this release's findings lived — the
// tool that was not registered, and the permission that a pipe has nobody
// to answer.

// thePlan is small on purpose: two items and a barrier is four agent
// turns, which is enough to prove the stages really ran and cheap enough
// to be a release gate.
const thePlan = `{
  "goal": "check the thing",
  "stages": [
    { "name": "find", "kind": "fanout", "agent": "oracle", "role": "readonly",
      "over": ["alpha", "beta"],
      "prompt": "Look at {{item}}.",
      "returns": { "findings": "strings" } },
    { "name": "report", "kind": "barrier", "agent": "plan",
      "prompt": "Write up:\n{{input}}" }
  ]
}`

// planModel plays three parts, told apart by what each request carries
// rather than by call order, because the stages run at the same time.
//
//   - The orchestrator has the Orchestrate tool. It calls it once and
//     then answers.
//   - A stage that declared what it returns has the Answer tool, and must
//     use it exactly once: a model that answers again every time it is
//     asked is a stage that never ends, which is a fake worth not
//     writing.
//   - A stage that declared nothing has neither, and just replies.
type planModel struct {
	mu           sync.Mutex
	orchestrated int
	stagePrompts []string
	toolResults  []string
}

func (m *planModel) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.Unmarshal(raw, &body)

		has := map[string]bool{}
		for _, tl := range body.Tools {
			has[tl.Function.Name] = true
		}
		user, spoken := "", false
		for _, msg := range body.Messages {
			switch msg.Role {
			case "user":
				var s string
				if json.Unmarshal(msg.Content, &s) == nil {
					user = s
				}
			case "assistant":
				// This conversation has already made its one tool call.
				// Read off the request rather than remembered, because
				// several conversations share this one server.
				if strings.Contains(string(msg.Content), "tool_call") ||
					strings.Contains(string(raw), `"role":"tool"`) {
					spoken = true
				}
			case "tool":
				spoken = true
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		defer w.(http.Flusher).Flush()

		m.mu.Lock()
		switch {
		case has["Orchestrate"]:
			for _, msg := range body.Messages {
				if msg.Role != "tool" {
					continue
				}
				// Unquoted. A tool message's content is a JSON string, so
				// the raw bytes are the escaped form — comparing against
				// those matches nothing containing a quote, silently.
				var text string
				if json.Unmarshal(msg.Content, &text) != nil {
					text = string(msg.Content)
				}
				m.toolResults = append(m.toolResults, text)
			}
			if !spoken {
				m.orchestrated++
				m.mu.Unlock()
				writeToolCall(w, "Orchestrate", thePlan)
				return
			}
		case has["Answer"]:
			m.stagePrompts = append(m.stagePrompts, user)
			if !spoken {
				m.mu.Unlock()
				writeToolCall(w, "Answer", `{"findings":["something"]}`)
				return
			}
		default:
			m.stagePrompts = append(m.stagePrompts, user)
		}
		m.mu.Unlock()
		writeText(w, "the answer")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeToolCall(w http.ResponseWriter, name, args string) {
	esc, _ := json.Marshal(args)
	fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":%q,\"arguments\":%s}}]}}]}\n\n", name, esc)
	fmt.Fprint(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func writeText(w http.ResponseWriter, s string) {
	enc, _ := json.Marshal(s)
	fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\n", enc)
	fmt.Fprint(w, "data: "+`{"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

const orchestrateOn = `, "smart_agent": true, "orchestrate": true`

// The whole feature, in the mode that has to be able to run unattended:
// the model writes a plan, the stages really run, and the report the
// model gets back is composed by localcode rather than by a model
// summarising its own success.
func TestAOneShotRunsAPlan(t *testing.T) {
	m := &planModel{}
	smartHome(t, m.server(t).URL, orchestrateOn)

	out, err := doRun(t, runOptions{
		format: formatText, agent: "general-purpose", skip: true,
	}, "check the thing")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.orchestrated != 1 {
		t.Fatalf("the model called Orchestrate %d times, want 1", m.orchestrated)
	}
	// Two fanout items and the barrier. Asserted as "each item was really
	// sent" rather than as a count, because a run that launched twice as
	// many agents as it should would also pass a count.
	for _, item := range []string{"Look at alpha.", "Look at beta."} {
		if !containsLine(m.stagePrompts, item) {
			t.Errorf("no stage was asked %q. Stages asked: %q", item, m.stagePrompts)
		}
	}
	if len(m.toolResults) != 1 {
		t.Fatalf("the orchestrator got %d tool results, want 1: %q", len(m.toolResults), m.toolResults)
	}
	report := m.toolResults[0]
	for _, want := range []string{
		"Orchestration finished: 3 agent turns across 2 stages",
		// Launched AND kept. Naming the stages is not enough: a run in
		// which every stage failed to answer in its declared shape names
		// them all the same way, which is what this assertion caught in
		// its own first draft.
		"## find (fanout, oracle): 2 launched, 2 kept",
		"## report (barrier, plan): 1 launched, 1 kept",
		// The declared shape itself, so the Answer tool is proved to have
		// been offered, called, and validated rather than fallen back on.
		`* oracle on alpha: {"findings":["something"]}`,
		`* oracle on beta: {"findings":["something"]}`,
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the run report does not contain %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "did not answer in the declared shape") {
		t.Errorf("a stage did not answer in its declared shape:\n%s", report)
	}
}

// The finding this test was written for. Orchestrate asks for permission
// every time, by design: a run is up to 32 agent turns and half an hour.
// A pipe has nobody to ask, so without --skip-permissions the model
// authors the whole plan and gets a refusal for it, and nothing runs.
//
// Pinned rather than fixed, because the gate is right and the flag is the
// documented way through it. What must not happen quietly is the other
// thing: a release in which the refusal stops arriving, or in which the
// flag stops being enough.
func TestAPlanNeedsPermissionThatAPipeCannotAnswer(t *testing.T) {
	m := &planModel{}
	smartHome(t, m.server(t).URL, orchestrateOn)

	if _, err := doRun(t, runOptions{
		format: formatText, agent: "general-purpose",
	}, "check the thing"); err != nil {
		t.Fatalf("run: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.orchestrated != 1 {
		t.Fatalf("the model called Orchestrate %d times, want 1", m.orchestrated)
	}
	if len(m.stagePrompts) != 0 {
		t.Errorf("a refused plan ran %d stages: %q", len(m.stagePrompts), m.stagePrompts)
	}
	if len(m.toolResults) != 1 {
		t.Fatalf("the orchestrator got %d tool results, want 1", len(m.toolResults))
	}
	// The model has to be able to tell "it did not run" from "it ran and
	// found nothing", or the turn reports a step it never took.
	if !strings.Contains(m.toolResults[0], "not run") {
		t.Errorf("the refusal does not say the plan did not run:\n%s", m.toolResults[0])
	}
}

func containsLine(all []string, want string) bool {
	for _, s := range all {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

// The other way through the gate, and the one that suits a machine that
// runs plans regularly: a permission rule in config.json rather than a
// flag on every invocation. Verified rather than assumed, because it is
// the setting a person would put in a file and then rely on.
func TestAnAllowRuleLetsAPipeRunAPlan(t *testing.T) {
	m := &planModel{}
	smartHome(t, m.server(t).URL, orchestrateOn+`, "permission": {"Orchestrate": "allow"}`)

	if _, err := doRun(t, runOptions{
		format: formatText, agent: "general-purpose",
	}, "check the thing"); err != nil {
		t.Fatalf("run: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.stagePrompts) == 0 {
		t.Fatalf("an allow rule for Orchestrate did not get the plan run; results: %q", m.toolResults)
	}
	if len(m.toolResults) != 1 || !strings.Contains(m.toolResults[0], "Orchestration finished") {
		t.Errorf("the run did not report finishing: %q", m.toolResults)
	}
}
