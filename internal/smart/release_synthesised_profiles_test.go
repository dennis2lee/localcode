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

// A profile synthesised from an opencode file does not take a lane from
// the profiles a person wrote.
//
// This test used to assert the opposite, and the opposite was a silent
// regression: a machine with an opencode config beside a localcode one
// got an opencode:default alongside the profiles it already had, and
// bestMatch classifies by model id and takes the first by name — so that
// one model could become the quick lane over the person's own, and flip
// Solo with it, which changes what the main model is told. Reading
// somebody's opencode file must not change how their own config routes.
func TestASynthesisedProfileDoesNotTakeALaneFromAHandWrittenOne(t *testing.T) {
	body := `{
		"provider": {"anthropic": {"npm": "@ai-sdk/anthropic"}},
		"model": "anthropic/claude-haiku-4-5",
		"profiles": {
			"handwritten-deep": {"provider": "anthropic", "model": "claude-opus-4-5"}
		}
	}`

	cfg, err := loadText(t, body)
	if err != nil {
		t.Fatalf("config failed to load: %v", err)
	}

	// opencode:default is haiku and would classify as quick. The
	// hand-written profile is what routes anyway.
	if got := ProfileFor(cfg, CategoryQuick); got == "opencode:default" {
		t.Errorf("ProfileFor(CategoryQuick) = %q: the opencode file took the lane", got)
	}
	if got := ProfileFor(cfg, CategoryDeep); got != "handwritten-deep" {
		t.Errorf("ProfileFor(CategoryDeep) = %q, want %q", got, "handwritten-deep")
	}
}

// With nothing hand-written to route to, the file's own model serves.
// A configuration made entirely of synthesised profiles still has to
// work; what it must not do is reach for a per-agent one.
func TestTheFilesOwnModelServesWhenThereIsNothingElse(t *testing.T) {
	body := `{
		"provider": {"anthropic": {"npm": "@ai-sdk/anthropic"}},
		"model": "anthropic/claude-haiku-4-5",
		"agent": {"review": {"model": "anthropic/claude-opus-4-5"}}
	}`
	cfg, err := loadText(t, body)
	if err != nil {
		t.Fatalf("config failed to load: %v", err)
	}
	for _, category := range Categories {
		got := ProfileFor(cfg, category)
		if got == "opencode:agent:review" {
			t.Errorf("ProfileFor(%q) = %q: a profile written for one agent became a lane", category, got)
		}
		if got != "opencode:default" {
			t.Errorf("ProfileFor(%q) = %q, want the file's own model", category, got)
		}
	}
	if !Solo(cfg) {
		t.Error("Solo(cfg) = false, and every lane resolves to the same profile")
	}
}

// An explicit default_profile is a person naming a model, and it is
// theirs whatever the name begins with. Passing over it and answering
// with whatever sorts first is worse than either honouring or refusing.
func TestAnExplicitDefaultProfileIsHonouredEvenWhenHeldBack(t *testing.T) {
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"a": {Type: "anthropic", APIKey: "k"}},
		DefaultProfile: "opencode:agent:own",
		Profiles: map[string]config.Profile{
			"aaa":                {Provider: "a", Model: "some-unclassifiable-model"},
			"opencode:agent:own": {Provider: "a", Model: "another-unclassifiable-model"},
		},
	}
	if got := ProfileFor(cfg, CategoryQuick); got != "opencode:agent:own" {
		t.Errorf("ProfileFor(quick) = %q, want the configured default_profile", got)
	}
}
