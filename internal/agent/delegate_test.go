package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// modelRecorder is a stub model endpoint that records which model IDs were
// requested, so a test can assert which model actually ran.
type modelRecorder struct {
	mu     sync.Mutex
	models []string
}

func (r *modelRecorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.models...)
}

func (r *modelRecorder) server(t *testing.T, reply string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(req.Body).Decode(&body)
		r.mu.Lock()
		r.models = append(r.models, body.Model)
		r.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", reply)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
}

// newDelegateLoop builds a loop with a big-model main agent and a
// small-model explore agent, plus a task manager (which is what makes
// delegation possible at all).
func newDelegateLoop(t *testing.T, modelURL string, match ...string) (*Loop, *session.Store) {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: modelURL},
		},
		Profiles: map[string]config.Profile{
			"big":   {Provider: "local", Model: "big-model"},
			"small": {Provider: "local", Model: "small-model"},
		},
		Agents: map[string]config.AgentConfig{
			"general-purpose": {Profile: "big"},
			"explore":         {Profile: "small", Description: "Fast read-only search."},
		},
		DefaultProfile:     "big",
		MaxConcurrentTasks: 2,
		AutoDelegate: &config.AutoDelegateConfig{
			Enabled: true, Agent: "explore", Match: match,
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	loop := New(store, tools.NewRegistry(nil),
		map[string]provider.Provider{"local": provider.NewOpenAICompat(modelURL, "")}, cfg)
	NewTaskManager(context.Background(), loop, 2) // sets loop.Tasks
	return loop, store
}

func runTurn(t *testing.T, loop *Loop, store *session.Store, sid, text string) []events.Event {
	t.Helper()
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", text); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	all, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	return all
}

func hasEvent(evs []events.Event, typ events.Type) bool {
	for _, e := range evs {
		if e.Type == typ {
			return true
		}
	}
	return false
}

// TestDelegatedPromptRunsOnTheCheapModelOnly is the headline behavior and
// the whole reason the feature exists: a matching prompt is answered by
// the sub-agent's model, and the main session's model is never called —
// so the main session's cached prefix is never invalidated by a model
// switch.
func TestDelegatedPromptRunsOnTheCheapModelOnly(t *testing.T) {
	rec := &modelRecorder{}
	model := rec.server(t, "found it in loop.go")
	defer model.Close()

	loop, store := newDelegateLoop(t, model.URL, "find *")
	evs := runTurn(t, loop, store, "s1", "find the config loader")

	seen := rec.seen()
	if len(seen) != 1 || seen[0] != "small-model" {
		t.Errorf("models called = %v, want exactly [small-model] — the main model must not run for a delegated turn", seen)
	}
	if !hasEvent(evs, events.TypeDelegated) {
		t.Error("expected a delegated event so the user can see a cheaper agent answered")
	}
}

// TestNonMatchingPromptStaysOnTheMainModel guards the other direction:
// delegation must not swallow ordinary prompts.
func TestNonMatchingPromptStaysOnTheMainModel(t *testing.T) {
	rec := &modelRecorder{}
	model := rec.server(t, "done")
	defer model.Close()

	loop, store := newDelegateLoop(t, model.URL, "find *")
	evs := runTurn(t, loop, store, "s1", "refactor the config loader")

	for _, m := range rec.seen() {
		if m != "big-model" {
			t.Errorf("models called = %v, want only big-model for a non-matching prompt", rec.seen())
			break
		}
	}
	if hasEvent(evs, events.TypeDelegated) {
		t.Error("a non-matching prompt should not be delegated")
	}
}

// TestDelegationOffKeepsEverythingOnTheMainModel covers the /config
// runtime toggle: turning it off must route matching prompts back to the
// main model without a restart.
func TestDelegationOffKeepsEverythingOnTheMainModel(t *testing.T) {
	rec := &modelRecorder{}
	model := rec.server(t, "done")
	defer model.Close()

	loop, store := newDelegateLoop(t, model.URL, "find *")
	loop.SetAutoDelegateEnabled(false)
	runTurn(t, loop, store, "s1", "find the config loader")

	for _, m := range rec.seen() {
		if m != "big-model" {
			t.Errorf("models called = %v, want only big-model once delegation is toggled off", rec.seen())
			break
		}
	}
}

// TestNoDelegationFromInsideADelegatedSession is the recursion guard. A
// sub-agent session's own prompt matches the same rule that sent it there,
// so without the parent check it would spawn a child, and so on forever.
func TestNoDelegationFromInsideADelegatedSession(t *testing.T) {
	rec := &modelRecorder{}
	model := rec.server(t, "done")
	defer model.Close()

	loop, store := newDelegateLoop(t, model.URL, "find *")
	// A child session, exactly the shape SpawnSync creates.
	if _, err := store.CreateSession("child", "parent", "explore", false); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, ok := loop.delegateTarget("child", "explore", "find the config loader"); ok {
		t.Error("a session with a parent must never delegate again — that recurses without end")
	}
}

// TestNoDelegationToTheAgentAlreadyRunning is the other recursion guard:
// the explore agent asking explore to explore.
func TestNoDelegationToTheAgentAlreadyRunning(t *testing.T) {
	rec := &modelRecorder{}
	model := rec.server(t, "done")
	defer model.Close()

	loop, store := newDelegateLoop(t, model.URL, "find *")
	if _, err := store.CreateSession("s1", "", "explore", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, ok := loop.delegateTarget("s1", "explore", "find the config loader"); ok {
		t.Error("delegating to the agent already running would recurse without end")
	}
}

// TestCommandsAreNeverDelegated: slash commands are handled before the
// delegation check, so a command that happens to match a pattern still
// runs as a command.
func TestCommandsAreNeverDelegated(t *testing.T) {
	rec := &modelRecorder{}
	model := rec.server(t, "done")
	defer model.Close()

	loop, store := newDelegateLoop(t, model.URL, "*") // deliberately matches everything
	evs := runTurn(t, loop, store, "s1", "/usage")

	if hasEvent(evs, events.TypeDelegated) {
		t.Error("/usage is a local command and must not be delegated even when the pattern matches everything")
	}
	if len(rec.seen()) != 0 {
		t.Errorf("models called = %v, want none — /usage answers locally", rec.seen())
	}
}
