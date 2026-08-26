package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"localcode/internal/config"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/smart"
	"localcode/internal/tools"
)

// smartServer answers every request with one line of text and records what
// it was sent, so these tests can assert on the system prompt and the tool
// list rather than on a scripted conversation.
func smartServer(t *testing.T) (*httptest.Server, func() []recordedRequest) {
	t.Helper()
	var mu sync.Mutex
	var requests []recordedRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string           `json:"model"`
			Messages []map[string]any `json:"messages"`
			Tools    []map[string]any `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		system := ""
		for _, m := range body.Messages {
			if m["role"] == "system" {
				if s, ok := m["content"].(string); ok {
					system = s
				}
			}
		}
		toolset := map[string]bool{}
		for _, tl := range body.Tools {
			if fn, ok := tl["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					toolset[name] = true
				}
			}
		}
		mu.Lock()
		requests = append(requests, recordedRequest{model: body.Model, system: system, toolsLen: len(body.Tools), toolset: toolset})
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))

	return srv, func() []recordedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedRequest(nil), requests...)
	}
}

// newSmartLoop builds a loop with one agent and two profiles — the config
// Smart Agent is aimed at, where there is nothing to delegate to until it
// is turned on.
func newSmartLoop(t *testing.T, modelURL string) *Loop {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	registry := tools.NewRegistry(nil)
	registry.Register(tools.ReadFile{})
	registry.Register(tools.Glob{})
	registry.Register(tools.Grep{})
	registry.Register(tools.Bash{})

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: modelURL},
		},
		Profiles: map[string]config.Profile{
			"strong": {Provider: "local", Model: "claude-opus-5"},
			"cheap":  {Provider: "local", Model: "claude-haiku-4-5"},
		},
		DefaultProfile: "strong",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	loop := New(store, registry, map[string]provider.Provider{"local": provider.NewOpenAICompat(modelURL, "")}, cfg)
	tasks := NewTaskManager(context.Background(), loop, 5)
	registry.Register(NewTaskTool(tasks, loop.DelegatableAgents))
	registry.Register(NewTaskBackgroundTool(tasks, loop.DelegatableAgents))
	registry.Register(NewTaskCollectTool(tasks))
	return loop
}

func sendOne(t *testing.T, loop *Loop, sid, agentName string) {
	t.Helper()
	if _, err := loop.Store.Get(sid); err != nil {
		if _, err := loop.Store.CreateSession(sid, "", agentName, true); err != nil {
			t.Fatalf("create session: %v", err)
		}
	}
	if err := loop.SendMessage(context.Background(), sid, agentName, "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
}

// Off is off. The delegation tools are registered unconditionally now (the
// roster they advertise changes at runtime, so the old "register it only if
// the config has two agents" rule could not be read at startup any more) —
// which means the hiding has to be real, or every existing single-agent
// config would suddenly grow three tools it has nothing to use them for.
func TestWithSmartAgentOffNothingChanges(t *testing.T) {
	srv, recorded := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)

	if loop.SmartAgentEnabled() {
		t.Fatal("Smart Agent defaulted to on")
	}
	sendOne(t, loop, "s1", "general-purpose")

	reqs := recorded()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	for _, name := range []string{"Task", "TaskBackground", "TaskCollect"} {
		if reqs[0].toolset[name] {
			t.Errorf("%s was offered with Smart Agent off and one agent configured", name)
		}
	}
	if strings.Contains(reqs[0].system, "Smart Agent is on") {
		t.Error("the orchestration prompt was sent with the feature off")
	}
}

func TestTurningItOnAddsTheRosterAndTheOrchestrationPrompt(t *testing.T) {
	srv, recorded := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)

	sendOne(t, loop, "s1", "general-purpose")

	reqs := recorded()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	for _, name := range []string{"Task", "TaskBackground", "TaskCollect"} {
		if !reqs[0].toolset[name] {
			t.Errorf("%s was not offered with Smart Agent on", name)
		}
	}
	if !strings.Contains(reqs[0].system, "Smart Agent is on") {
		t.Error("the orchestration prompt was not added to the system prompt")
	}
	// The roster reaches the model through the Task tool's description, so
	// a specialist that is not named there cannot be delegated to.
	spec := ""
	for _, tl := range loop.Tools.SpecsFor(nil) {
		if tl.Name == "Task" {
			spec = tl.Description + string(tl.InputSchema)
		}
	}
	for _, name := range []string{"explore", "librarian", "oracle", "plan", "implement", "verify"} {
		if !strings.Contains(spec, name) {
			t.Errorf("the Task tool does not offer %q", name)
		}
	}
}

// The roster is derived per turn rather than merged into the config at
// startup, because the switch is live. This is the test that says so: no
// restart, no reload, and the next turn sees the change.
func TestTheSwitchTakesEffectOnTheNextTurn(t *testing.T) {
	srv, recorded := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)

	sendOne(t, loop, "s1", "general-purpose")
	loop.SetSmartAgentEnabled(true)
	sendOne(t, loop, "s1", "general-purpose")
	loop.SetSmartAgentEnabled(false)
	sendOne(t, loop, "s1", "general-purpose")

	reqs := recorded()
	if len(reqs) != 3 {
		t.Fatalf("got %d requests, want 3", len(reqs))
	}
	if reqs[0].toolset["Task"] {
		t.Error("Task was offered before the setting was turned on")
	}
	if !reqs[1].toolset["Task"] {
		t.Error("Task was not offered on the turn after the setting was turned on")
	}
	if reqs[2].toolset["Task"] {
		t.Error("Task was still offered after the setting was turned off again")
	}
}

// A specialist is not an orchestrator. Giving it the policy as well as the
// tools would have it narrating a decomposition it has no way to carry
// out, in a context whose whole job is to answer one question briefly.
func TestASpecialistIsNotGivenTheOrchestrationPrompt(t *testing.T) {
	srv, recorded := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)

	sendOne(t, loop, "child", "explore")

	reqs := recorded()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	if strings.Contains(reqs[0].system, "Smart Agent is on") {
		t.Error("the explore agent was told to orchestrate")
	}
	if !strings.Contains(reqs[0].system, "You are the explore agent") {
		t.Error("the explore agent did not get its own prompt")
	}
	for _, name := range []string{"Task", "TaskBackground", "TaskCollect", "bash"} {
		if reqs[0].toolset[name] {
			t.Errorf("the explore agent was offered %s", name)
		}
	}
	if !reqs[0].toolset["grep"] {
		t.Error("the explore agent was not offered grep")
	}
	// Routed to the cheap model, which is most of the point: an expensive
	// model grepping is the cost this feature exists to avoid.
	if reqs[0].model != "claude-haiku-4-5" {
		t.Errorf("explore ran on %q, want the cheap model", reqs[0].model)
	}
}

// A user's own agent run as somebody else's sub-agent. The check above
// cannot see this one — the agent is not in the built-in roster — so the
// parent id is what catches it.
func TestAChildSessionIsNotGivenTheOrchestrationPrompt(t *testing.T) {
	srv, recorded := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)

	if _, err := loop.Store.CreateSession("parent", "", "general-purpose", true); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if _, err := loop.Store.CreateSession("kid", "parent", "general-purpose", false); err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := loop.SendMessage(context.Background(), "kid", "general-purpose", "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	reqs := recorded()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	if strings.Contains(reqs[0].system, "Smart Agent is on") {
		t.Error("a child session was told to orchestrate")
	}
}

// The allowlist is enforced where the call happens, not only in what the
// model was shown. A model that asks for a tool it was not offered — a
// stale cached prefix, a confused local model — must be refused rather
// than served.
func TestASpecialistThatCallsADelegationToolIsRefused(t *testing.T) {
	srv, _ := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)
	loop.SetSmartAgentEnabled(true)

	if _, err := loop.Store.CreateSession("child", "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	allowed := loop.toolsForTurn(loop.agentConfig("explore"))
	for _, name := range []string{"Task", "TaskBackground", "TaskCollect", "bash"} {
		if tools.IsAllowed(allowed, name) {
			t.Errorf("%s would be allowed to run for the explore agent", name)
		}
	}
	if !tools.IsAllowed(allowed, "grep") {
		t.Error("grep would be refused for the explore agent")
	}
}

// The other half of the name guard internal/smart cannot make: the
// delegation tools it names by string are the ones this package actually
// registers. A rename on either side is otherwise silent — the tool is
// registered under its new name and hidden under nothing.
func TestTheDelegationToolNamesMatchWhatIsRegistered(t *testing.T) {
	registered := map[string]bool{
		NewTaskTool(nil, nil).Name():           true,
		NewTaskBackgroundTool(nil, nil).Name(): true,
		NewTaskCollectTool(nil).Name():         true,
	}
	for _, name := range smart.DelegationTools {
		if !registered[name] {
			t.Errorf("internal/smart hides %q, but no tool registers under that name", name)
		}
	}
	if len(registered) != len(smart.DelegationTools) {
		t.Errorf("there are %d delegation tools but internal/smart names %d", len(registered), len(smart.DelegationTools))
	}
}

// The stable prefix is marked only when the bundle is on, because a cache
// breakpoint changes the shape of the request on the wire. It is harmless
// where it is not honoured, and it is still not a change to make to
// everyone's requests silently.
func TestTheCacheBreakpointFollowsTheSwitch(t *testing.T) {
	var cachePrefix []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The openai-compat wire format has nothing to carry a breakpoint,
		// so this checks the request localcode built rather than the JSON
		// it sent. The provider-side shape has its own tests.
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	loop := newSmartLoop(t, srv.URL)
	recorder := &cacheRecordingProvider{inner: loop.Providers["local"], seen: &cachePrefix}
	loop.Providers["local"] = recorder

	sendOne(t, loop, "s1", "general-purpose")
	loop.SetSmartAgentEnabled(true)
	sendOne(t, loop, "s1", "general-purpose")

	if len(cachePrefix) != 2 {
		t.Fatalf("saw %d requests, want 2", len(cachePrefix))
	}
	if cachePrefix[0] {
		t.Error("a cache breakpoint was asked for with the feature off")
	}
	if !cachePrefix[1] {
		t.Error("no cache breakpoint was asked for with the feature on")
	}
}

type cacheRecordingProvider struct {
	inner provider.Provider
	seen  *[]bool
}

func (p *cacheRecordingProvider) Chat(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	*p.seen = append(*p.seen, req.CachePrefix)
	return p.inner.Chat(ctx, req)
}
