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
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// A fallback that moves a turn from a model that can see to one that
// cannot takes view_image off the list, as a turn that started on the
// second model would never have had it. The image itself would be refused
// anyway (see takeImages); a tool the model is offered and can only fail
// with is a turn spent finding that out.
func TestAFallbackToAModelThatCannotSeeIsNotOfferedTheTool(t *testing.T) {
	var mu sync.Mutex
	type toolReq struct {
		model string
		tools []string
	}
	var reqs []toolReq
	seen := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
			Tools []struct {
				Type     string `json:"type"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		var toolNames []string
		for _, tool := range req.Tools {
			toolNames = append(toolNames, tool.Function.Name)
		}
		mu.Lock()
		reqs = append(reqs, toolReq{model: req.Model, tools: toolNames})
		n := seen
		seen++
		mu.Unlock()

		if n == 0 {
			// The first model is not there, so the turn falls back.
			http.Error(w, `{"error":{"message":"model not found"}}`, http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answered\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"local": {Type: config.ProviderOpenAICompat, BaseURL: srv.URL},
		},
		Profiles: map[string]config.Profile{
			"primary": {Provider: "local", Model: "claude-opus-5", Fallback: []string{"backup"}},
			"backup":  {Provider: "local", Model: "qwen3-coder-30b"}, // cannot see
		},
		Agents: map[string]config.AgentConfig{
			"general-purpose": {Profile: "primary"},
		},
		DefaultProfile: "primary",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}

	reg := tools.NewRegistry(nil)
	reg.Register(tools.ViewImage{})
	reg.Register(tools.ReadFile{})

	loop := New(store, reg, map[string]provider.Provider{"local": provider.NewOpenAICompat(srv.URL, "")}, cfg)
	loop.SetSmartAgentEnabled(true)

	if _, err := store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	err = loop.SendMessage(context.Background(), "s1", "general-purpose", "look at this")
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(reqs) < 2 {
		t.Fatalf("expected at least 2 requests (primary and fallback), got %d", len(reqs))
	}

	has := func(list []string, name string) bool {
		for _, n := range list {
			if n == name {
				return true
			}
		}
		return false
	}
	if !has(reqs[0].tools, tools.ViewImageName) {
		t.Fatalf("the Claude model was not offered view_image: %v", reqs[0].tools)
	}
	if has(reqs[1].tools, tools.ViewImageName) {
		t.Errorf("the fallback model %q, which cannot see, was offered view_image: %v", reqs[1].model, reqs[1].tools)
	}
	if !has(reqs[1].tools, "read_file") {
		t.Errorf("the fallback lost tools it should keep: %v", reqs[1].tools)
	}
}

// The other way a turn falls back: a stream that died before saying
// anything. The same rule holds there.
func TestAFallbackAfterADeadStreamIsNotOfferedTheTool(t *testing.T) {
	reg := tools.NewRegistry(nil)
	reg.Register(tools.ReadFile{})
	reg.Register(tools.ViewImage{})
	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		{{Type: provider.EventError, Err: fmt.Errorf("openai-compat endpoint returned 404: model not found")}},
		textReply("answered"),
	}}
	loop, sessionID := scriptedLoop(t, p, reg)
	loop.Config.Profiles["primary"] = config.Profile{Provider: "local", Model: "claude-opus-5", Fallback: []string{"backup"}}
	loop.Config.Profiles["backup"] = config.Profile{Provider: "local", Model: "qwen3-coder-30b"}
	loop.Config.Agents["general-purpose"] = config.AgentConfig{Profile: "primary"}
	loop.SetSmartAgentEnabled(true)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "look"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	reqs := requestsOf(p)
	if len(reqs) != 2 {
		t.Fatalf("%d requests, want the first model and the fallback", len(reqs))
	}
	if !offersTool(reqs[0], tools.ViewImageName) {
		t.Fatal("the Claude model was not offered view_image")
	}
	if offersTool(reqs[1], tools.ViewImageName) {
		t.Error("the fallback model, which cannot see, was offered view_image after a dead stream")
	}
}

// A second fallback, to a model that can see, has the tool back: each
// fallback's list is the turn's own, narrowed for the model it moves to,
// not the last fallback's list narrowed again.
func TestASecondFallbackToAModelThatCanSeeHasTheToolBack(t *testing.T) {
	reg := tools.NewRegistry(nil)
	reg.Register(tools.ReadFile{})
	reg.Register(tools.ViewImage{})
	dead := []provider.StreamEvent{{Type: provider.EventError, Err: fmt.Errorf("openai-compat endpoint returned 404: model not found")}}
	p := &scriptedProvider{turns: [][]provider.StreamEvent{dead, dead, textReply("answered")}}
	loop, sessionID := scriptedLoop(t, p, reg)
	yes := true
	loop.Config.Profiles["primary"] = config.Profile{Provider: "local", Model: "claude-opus-5", Fallback: []string{"blind", "seeing"}}
	loop.Config.Profiles["blind"] = config.Profile{Provider: "local", Model: "qwen3-coder-30b"}
	loop.Config.Profiles["seeing"] = config.Profile{Provider: "local", Model: "qwen2.5-vl-72b", Vision: &yes}
	loop.Config.Agents["general-purpose"] = config.AgentConfig{Profile: "primary"}
	loop.SetSmartAgentEnabled(true)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "look"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	reqs := requestsOf(p)
	if len(reqs) != 3 {
		t.Fatalf("%d requests, want three models in turn", len(reqs))
	}
	for i, want := range []bool{true, false, true} {
		if got := offersTool(reqs[i], tools.ViewImageName); got != want {
			t.Errorf("request %d (%s): view_image offered = %v, want %v", i, reqs[i].Model, got, want)
		}
	}
}
