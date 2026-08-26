package smart

import (
	"strings"
	"testing"

	"localcode/internal/config"
)

func twoModelConfig() *config.Config {
	return &config.Config{
		Profiles: map[string]config.Profile{
			"big":   {Provider: "p", Model: "claude-opus-5"},
			"small": {Provider: "p", Model: "claude-haiku-4-5"},
		},
		DefaultProfile: "big",
	}
}

// The guard that a prompt cannot give. A sub-agent handed the delegation
// tools and told to be thorough will use them, and each one it spawns can
// spawn more; the depth counter bounds how deep that goes but not how wide.
// Leaving the tools out of the allowlist is what actually stops it, and
// this is the test that notices when a new specialist is added without it.
func TestNoSpecialistCanDelegate(t *testing.T) {
	forbidden := map[string]bool{}
	for _, name := range DelegationTools {
		forbidden[name] = true
	}
	for _, a := range Builtins() {
		if len(a.Tools) == 0 {
			t.Errorf("%s has no tool allowlist, so it gets every tool including the delegation ones", a.Name)
			continue
		}
		for _, tool := range a.Tools {
			if forbidden[tool] {
				t.Errorf("%s may call %s: a sub-agent that can delegate can spawn sub-agents that can delegate", a.Name, tool)
			}
		}
	}
}

// A read-only specialist with a shell is not read-only. The whole value of
// sending investigation elsewhere is that its answer can be trusted not to
// have changed anything on the way, and `sh -c` is a write tool.
func TestTheInvestigatingAgentsHaveNoShell(t *testing.T) {
	investigators := map[string]bool{"explore": true, "librarian": true, "oracle": true, "plan": true}
	for _, a := range Builtins() {
		if !investigators[a.Name] {
			continue
		}
		for _, tool := range a.Tools {
			if tool == ToolBash || tool == ToolWrite || tool == ToolEdit {
				t.Errorf("%s is meant to be read-only but may call %s", a.Name, tool)
			}
		}
	}
}

func TestEachSpecialistIsRoutedToAProfile(t *testing.T) {
	agents := Agents(twoModelConfig())
	if len(agents) != len(Builtins()) {
		t.Fatalf("got %d agents, want %d", len(agents), len(Builtins()))
	}
	for name, a := range agents {
		if a.Profile == "" {
			t.Errorf("%s has no profile", name)
		}
		if a.Description == "" {
			t.Errorf("%s has no description; the orchestrator picks by it", name)
		}
		if !strings.Contains(a.Prompt, "Report back in under") {
			t.Errorf("%s is not told to answer briefly, so its whole transcript lands in the caller's context", name)
		}
	}
	if got := agents["explore"].Profile; got != "small" {
		t.Errorf("explore routed to %q, want the cheap model", got)
	}
	if got := agents["oracle"].Profile; got != "big" {
		t.Errorf("oracle routed to %q, want the strong model", got)
	}
}

// Somebody who has written their own "explore" agent keeps it. Overwriting
// it would be the feature silently replacing a deliberate choice with a
// generic one, which is worse than not shipping the specialist at all.
func TestAUserDefinedAgentOfTheSameNameIsLeftAlone(t *testing.T) {
	cfg := twoModelConfig()
	cfg.Agents = map[string]config.AgentConfig{
		"explore": {Profile: "big", Prompt: "mine", Description: "mine too"},
	}
	agents := Agents(cfg)
	if _, replaced := agents["explore"]; replaced {
		t.Error("the built-in explore agent was offered even though the user defines one")
	}
	if _, ok := agents["oracle"]; !ok {
		t.Error("the other specialists should still be offered")
	}
}

// Turning Smart Agent on before any model is configured is legal and does
// nothing, rather than producing six agents whose profile does not resolve
// and which fail the moment they are delegated to.
func TestNoProfilesMeansNoRoster(t *testing.T) {
	if got := Agents(&config.Config{}); got != nil {
		t.Errorf("got %v, want no agents at all", got)
	}
	if got := Agents(nil); got != nil {
		t.Errorf("got %v for a nil config, want none", got)
	}
}

func TestNamesMatchTheRoster(t *testing.T) {
	names := Names()
	if len(names) != len(Builtins()) {
		t.Fatalf("Names() has %d entries, the roster has %d", len(names), len(Builtins()))
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] >= names[i] {
			t.Errorf("Names() is not sorted: %q before %q", names[i-1], names[i])
		}
	}
}
