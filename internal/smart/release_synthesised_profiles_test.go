package smart

import (
	"os"
	"path/filepath"
	"testing"

	"localcode/internal/config"
)

// loadText writes body to a temporary config.json and loads it via config.Load.
func loadText(t *testing.T, body string) (*config.Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Load(p)
	return cfg, err
}

// TestOpencodeAgentProfileExcludedFromSmartRouting verifies that a synthesised
// opencode:agent:* profile does NOT become a Smart Agent lane and does NOT alter
// Solo(cfg).
//
// Rationale: A profile synthesised from one agent's own model (e.g. agent.quick.model: "...haiku...")
// belongs strictly to that agent. If routing considered it, bestMatch would pick opencode:agent:quick
// as the quick lane for every grep, glob, and build specialist across the entire roster, and Solo(cfg)
// would flip from true to false — changing what the main model is told in OrchestrationPrompt
// and PlanPolicy.
func TestOpencodeAgentProfileExcludedFromSmartRouting(t *testing.T) {
	body := `{
		"provider": {
			"anthropic": {
				"npm": "@ai-sdk/anthropic"
			}
		},
		"model": "anthropic/claude-opus-4-5",
		"agent": {
			"quick": {
				"description": "Agent for quick tasks",
				"model": "anthropic/claude-haiku-4-5"
			}
		}
	}`

	cfg, err := loadText(t, body)
	if err != nil {
		t.Fatalf("config failed to load: %v", err)
	}

	// Assert through smart.ProfileFor that opencode:agent:quick was NOT chosen for CategoryQuick.
	quickProfile := ProfileFor(cfg, CategoryQuick)
	if quickProfile == "opencode:agent:quick" {
		t.Errorf("ProfileFor(CategoryQuick) = %q, want synthesised agent profile excluded from routing", quickProfile)
	}
	if quickProfile != "opencode:default" {
		t.Errorf("ProfileFor(CategoryQuick) = %q, want %q", quickProfile, "opencode:default")
	}

	// Assert through smart.Solo that Solo remains true: with only opencode:default eligible,
	// all specialist categories resolve to the same profile (the model delegating to itself).
	if !Solo(cfg) {
		t.Errorf("Solo(cfg) = false, want true when only opencode:default is eligible for routing")
	}
}

// TestOpencodeDefaultProfileEligibleForSmartRouting verifies that opencode:default
// IS eligible for smart routing and can become a lane alongside other profiles.
func TestOpencodeDefaultProfileEligibleForSmartRouting(t *testing.T) {
	body := `{
		"provider": {
			"anthropic": {
				"npm": "@ai-sdk/anthropic"
			}
		},
		"model": "anthropic/claude-haiku-4-5",
		"profiles": {
			"handwritten-deep": {
				"provider": "anthropic",
				"model": "claude-opus-4-5"
			}
		}
	}`

	cfg, err := loadText(t, body)
	if err != nil {
		t.Fatalf("config failed to load: %v", err)
	}

	// opencode:default is haiku, so it matches CategoryQuick.
	if got := ProfileFor(cfg, CategoryQuick); got != "opencode:default" {
		t.Errorf("ProfileFor(CategoryQuick) = %q, want %q", got, "opencode:default")
	}

	// handwritten-deep is opus, so it matches CategoryDeep.
	if got := ProfileFor(cfg, CategoryDeep); got != "handwritten-deep" {
		t.Errorf("ProfileFor(CategoryDeep) = %q, want %q", got, "handwritten-deep")
	}

	// With different profiles for quick and deep, Solo must be false.
	if Solo(cfg) {
		t.Errorf("Solo(cfg) = true, want false when categories resolve to different profiles")
	}
}
