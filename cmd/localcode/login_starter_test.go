package main

import (
	"os"
	"path/filepath"
	"testing"

	"localcode/internal/config"
)

// What `localcode login anthropic` tells a new user to write is a config
// that loads and answers a first message. It used to print a provider
// block alone, which started and then failed the first message with "no
// profile for agent and no default_profile set".
func TestTheLoginStarterConfigLoadsAndAnswers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(anthropicStarterConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load(path)
	if err != nil {
		t.Fatalf("the suggested config does not load: %v", err)
	}
	p, err := cfg.ResolveProfile("general-purpose")
	if err != nil {
		t.Fatalf("the default agent has no profile: %v", err)
	}
	if pc := cfg.Providers[p.Provider]; pc.Type != config.ProviderAnthropic || pc.APIKey != "" {
		t.Errorf("profile runs on %+v, want the anthropic provider with no key, so the saved one is used", pc)
	}
}

// A run on a minimal config, which declares no agents, answers as the
// default agent on default_profile, as the daemon does. It was refused
// with "no agent general-purpose in config. Configured: " and nothing
// after the colon. A name somebody typed is still checked.
func TestARunOnAMinimalConfigAnswersAsTheDefaultAgent(t *testing.T) {
	load := func() *config.Config {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(anthropicStarterConfig), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, _, err := config.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	if name, err := applyModelChoice(load(), runOptions{agent: defaultRunAgent}); err != nil || name != defaultRunAgent {
		t.Errorf("the default agent: %q, %v", name, err)
	}
	cfg := load()
	if _, err := applyModelChoice(cfg, runOptions{agent: defaultRunAgent, model: "claude-opus-5"}); err != nil {
		t.Fatalf("--model on the default agent: %v", err)
	}
	if got := cfg.Profiles[cfg.DefaultProfile].Model; got != "claude-opus-5" {
		t.Errorf("--model applied to %q, want the default profile's model replaced", got)
	}
	if _, err := applyModelChoice(load(), runOptions{agent: defaultRunAgent, profile: "main"}); err != nil {
		t.Errorf("--profile on an undeclared default agent: %v", err)
	}
	if _, err := applyModelChoice(load(), runOptions{agent: "genral-purpose"}); err == nil {
		t.Error("a mistyped agent ran on the default profile")
	}
}
