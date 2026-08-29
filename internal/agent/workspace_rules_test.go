package agent

import (
	"context"
	"strings"
	"testing"

	"localcode/internal/config"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// Each session's turn carries the rules of the project that session is in.
//
// They used to be read once, at startup, from the process's own working
// directory: a session working anywhere else was told that project's
// AGENTS.md/CLAUDE.md did not exist and the startup directory's did, and a
// desktop build launched from Finder (working directory "/") had no project
// rules at all however many workspaces were open.
func TestEachSessionGetsItsOwnWorkspaceRules(t *testing.T) {
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.CreateSessionIn("s-alpha", "", "general-purpose", "/projects/alpha", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := store.CreateSessionIn("s-beta", "", "general-purpose", "/projects/beta", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1"}},
		Profiles:       map[string]config.Profile{"balanced": {Provider: "local", Model: "m"}},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "balanced"}},
		DefaultProfile: "balanced",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}

	reply := []provider.StreamEvent{
		{Type: provider.EventTextDelta, TextDelta: "ok"},
		{Type: provider.EventMessageStop, StopReason: "end_turn"},
	}
	p := &scriptedProvider{turns: [][]provider.StreamEvent{reply, reply}}

	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{"local": p}, cfg)
	loop.ProjectDir = "/somewhere/else"
	loop.WorkspaceRules = func(dir string) string { return "rules of " + dir }

	for _, id := range []string{"s-alpha", "s-beta"} {
		if err := loop.SendMessage(context.Background(), id, "general-purpose", "hi"); err != nil {
			t.Fatalf("SendMessage(%s): %v", id, err)
		}
	}

	if len(p.systemPrompts()) != 2 {
		t.Fatalf("provider saw %d requests, want 2", len(p.systemPrompts()))
	}
	if !strings.Contains(p.systemPrompts()[0], "rules of /projects/alpha") {
		t.Errorf("first system prompt = %q, want alpha's rules", p.systemPrompts()[0])
	}
	if !strings.Contains(p.systemPrompts()[1], "rules of /projects/beta") {
		t.Errorf("second system prompt = %q, want beta's rules", p.systemPrompts()[1])
	}
	if strings.Contains(p.systemPrompts()[1], "/projects/alpha") {
		t.Errorf("second system prompt still carries the other session's rules: %q", p.systemPrompts()[1])
	}
}

// A model whose habits are wrong for this window is told so, and only
// that model is.
//
// Gemma writes arrows and names as LaTeX (`$\rightarrow$`,
// `$\text{Bla}$`), which renders as itself in a client with MathJax and
// as dollars and backslashes here.
func TestOnlyTheModelsThatNeedItGetAFormattingNote(t *testing.T) {
	for _, tt := range []struct {
		model string
		want  bool
	}{
		{"gemma-3-27b-it-q4_K_M", true},
		{"google/Gemma-2-9B", true},
		{"qwen3-30b-a3b", false},
		{"claude-opus-5", false},
	} {
		store, err := session.NewStore("")
		if err != nil {
			t.Fatalf("new store: %v", err)
		}
		if _, err := store.CreateSession("s-1", "", "general-purpose", true); err != nil {
			t.Fatalf("create session: %v", err)
		}
		cfg := &config.Config{
			Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1"}},
			Profiles:       map[string]config.Profile{"balanced": {Provider: "local", Model: tt.model}},
			Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "balanced"}},
			DefaultProfile: "balanced",
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("invalid config: %v", err)
		}
		p := &scriptedProvider{turns: [][]provider.StreamEvent{{
			{Type: provider.EventTextDelta, TextDelta: "ok"},
			{Type: provider.EventMessageStop, StopReason: "end_turn"},
		}}}
		loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{"local": p}, cfg)

		if err := loop.SendMessage(context.Background(), "s-1", "general-purpose", "hi"); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
		if len(p.systemPrompts()) != 1 {
			t.Fatalf("provider saw %d requests, want 1", len(p.systemPrompts()))
		}
		got := strings.Contains(p.systemPrompts()[0], "there is no LaTeX or MathJax")
		if got != tt.want {
			t.Errorf("model %q: formatting note present = %v, want %v", tt.model, got, tt.want)
		}
	}
}
