package config

import (
	"context"
	"testing"
)

// Whether a profile's model is sent images. The default is a guess by name
// for Claude only, because an image sent to a model that cannot take one is
// a request its server refuses on every turn after; anything else has to
// be told.
func TestViewsImages(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name   string
		model  string
		vision *bool
		want   bool
	}{
		{"Claude by name", "claude-sonnet-5-5", nil, true},
		{"Claude through Bedrock", "us.anthropic.claude-opus-5-v1:0", nil, true},
		{"Claude in any case", "Claude-Haiku-5-5", nil, true},
		{"anything else by default", "deepseek-v4.1-flash", nil, false},
		{"a vision model by name is still not guessed", "qwen2.5-vl-72b", nil, false},
		{"no model at all", "", nil, false},
		{"said on", "qwen2.5-vl-72b", &yes, true},
		{"said off, even for Claude", "claude-sonnet-5-5", &no, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (Profile{Model: c.model, Vision: c.vision}).ViewsImages(); got != c.want {
				t.Errorf("ViewsImages() = %v, want %v", got, c.want)
			}
		})
	}
}

// OrchestrateFor with no config reads as off rather than panicking: the
// task manager asks it for every task it spawns, including from a loop
// built without a config. It did panic, once. A pin wins over both.
func TestOrchestrateForWithNoConfig(t *testing.T) {
	if OrchestrateFor(context.Background(), nil) {
		t.Error("no config read as on")
	}
	if !OrchestrateFor(WithOrchestrate(context.Background(), true), nil) {
		t.Error("a pinned on was not read with no config")
	}
	var nilCtx context.Context
	if on, ok := OrchestratePinned(nilCtx); on || ok {
		t.Errorf("a nil context reads as pinned (%v, %v)", on, ok)
	}
}
