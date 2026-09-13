package smart

import (
	"strings"
	"testing"
)

// The two tables promptVariants and planVariants govern top-level system
// prompt customization under Smart Agent. One selects the orchestrator
// prompt addition; the other selects the plan policy for the Orchestrate
// tool. They are parallel: each model family that receives a tailored
// orchestration prompt receives the matching plan policy threshold.
//
// If the two tables diverge in length, key ordering, or coverage, one half
// of Smart Agent will classify a model under its family while the other
// silently falls back to the default base policy.
func TestPromptAndPlanVariantsStayParallel(t *testing.T) {
	if len(promptVariants) != len(planVariants) {
		t.Fatalf("promptVariants has %d entries but planVariants has %d; tables must remain 1:1 parallel",
			len(promptVariants), len(planVariants))
	}
	for i := range promptVariants {
		if promptVariants[i].match != planVariants[i].match {
			t.Errorf("entry %d mismatch: promptVariants has match %q, but planVariants has match %q",
				i, promptVariants[i].match, planVariants[i].match)
		}
	}
}

// Both tables are evaluated first-match-wins via substring containment on
// the lowercased model ID. If an earlier entry is a substring of a later
// entry — for example, placing "gpt-" before "gpt-oss" — the later entry is
// permanently shadowed and dead code: every model containing the longer
// identifier also contains the shorter prefix and matches first.
//
// This test walks every pair (i, j) where i < j and verifies that no earlier
// entry's match string is contained within any later entry's match string.
func TestNoPromptVariantIsShadowedByAnEarlierPrefix(t *testing.T) {
	for i := 0; i < len(promptVariants); i++ {
		for j := i + 1; j < len(promptVariants); j++ {
			earlier := promptVariants[i].match
			later := promptVariants[j].match
			if strings.Contains(later, earlier) {
				t.Errorf("promptVariants[%d] %q shadows promptVariants[%d] %q: every model containing %q also contains %q and matches earlier",
					i, earlier, j, later, later, earlier)
			}
		}
	}

	for i := 0; i < len(planVariants); i++ {
		for j := i + 1; j < len(planVariants); j++ {
			earlier := planVariants[i].match
			later := planVariants[j].match
			if strings.Contains(later, earlier) {
				t.Errorf("planVariants[%d] %q shadows planVariants[%d] %q: every model containing %q also contains %q and matches earlier",
					i, earlier, j, later, later, earlier)
			}
		}
	}
}

// Model expectation pairs a recognized family key with a concrete,
// real-world model ID that a user would configure in a profile.
//
// Keying tests on the implementation's own match strings is how bugs survive
// undetected (testing a pattern through its own spelling). Stating the
// requirement means asserting that real models in the wild resolve to the
// prompt and plan variants their architectural tier demands.
type familyExpectation struct {
	matchKey   string
	realModel  string
	wantPrompt string
	wantPlan   string
}

var expectedModelFamilies = []familyExpectation{
	{
		matchKey:   "gpt-oss",
		realModel:  "gpt-oss-12b",
		wantPrompt: localVariant,
		wantPlan:   localPlanPolicy,
	},
	{
		matchKey:   "gpt-",
		realModel:  "gpt-4o",
		wantPrompt: gptVariant,
		wantPlan:   gptPlanPolicy,
	},
	{
		matchKey:   "o3",
		realModel:  "o3-mini",
		wantPrompt: gptVariant,
		wantPlan:   gptPlanPolicy,
	},
	{
		matchKey:   "o4",
		realModel:  "o4-mini",
		wantPrompt: gptVariant,
		wantPlan:   gptPlanPolicy,
	},
	{
		matchKey:   "gemini",
		realModel:  "gemini-2.5-pro",
		wantPrompt: geminiVariant,
		wantPlan:   geminiPlanPolicy,
	},
	{
		matchKey:   "qwen",
		realModel:  "qwen2.5-coder-32b",
		wantPrompt: localVariant,
		wantPlan:   localPlanPolicy,
	},
	{
		matchKey:   "glm",
		realModel:  "glm-4-9b-chat",
		wantPrompt: localVariant,
		wantPlan:   localPlanPolicy,
	},
	{
		matchKey:   "kimi",
		realModel:  "kimi-k1.5",
		wantPrompt: localVariant,
		wantPlan:   localPlanPolicy,
	},
	{
		matchKey:   "llama",
		realModel:  "llama-3.3-70b-instruct",
		wantPrompt: localVariant,
		wantPlan:   localPlanPolicy,
	},
	{
		matchKey:   "mistral",
		realModel:  "mistral-large-2411",
		wantPrompt: localVariant,
		wantPlan:   localPlanPolicy,
	},
	{
		matchKey:   "mixtral",
		realModel:  "mixtral-8x22b-instruct",
		wantPrompt: localVariant,
		wantPlan:   localPlanPolicy,
	},
	{
		matchKey:   "gemma",
		realModel:  "gemma-2-27b-it",
		wantPrompt: localVariant,
		wantPlan:   localPlanPolicy,
	},
	{
		matchKey:   "phi",
		realModel:  "phi-4",
		wantPrompt: localVariant,
		wantPlan:   localPlanPolicy,
	},
	{
		matchKey:   "deepseek",
		realModel:  "deepseek-v3",
		wantPrompt: localVariant,
		wantPlan:   localPlanPolicy,
	},
	{
		matchKey:   "glimmer",
		realModel:  "glimmer-9b",
		wantPrompt: localVariant,
		wantPlan:   localPlanPolicy,
	},
	{
		matchKey:   "muse",
		realModel:  "muse-30b",
		wantPrompt: localVariant,
		wantPlan:   localPlanPolicy,
	},
	{
		matchKey:   "devstral",
		realModel:  "devstral-24b",
		wantPrompt: localVariant,
		wantPlan:   localPlanPolicy,
	},
	{
		matchKey:   "granite",
		realModel:  "granite-3.1-8b-instruct",
		wantPrompt: localVariant,
		wantPlan:   localPlanPolicy,
	},
}

// Every recognized model family must be explicitly accounted for in the
// test inventory, and every real model ID must resolve to the expected prompt
// and plan variants.
func TestEveryKnownModelFamilyResolvesToItsIntendedPromptVariant(t *testing.T) {
	if len(promptVariants) != len(expectedModelFamilies) {
		t.Fatalf("promptVariants has %d entries, but expectedModelFamilies has %d; all entries must be accounted for",
			len(promptVariants), len(expectedModelFamilies))
	}

	for i, exp := range expectedModelFamilies {
		if promptVariants[i].match != exp.matchKey {
			t.Errorf("entry %d match key is %q, want %q", i, promptVariants[i].match, exp.matchKey)
		}

		gotPrompt := OrchestrationPrompt(exp.realModel, false)
		if gotPrompt != exp.wantPrompt {
			t.Errorf("model %q (family %q) got unexpected prompt variant; want %v (len %d), got len %d",
				exp.realModel, exp.matchKey, exp.wantPrompt[:30], len(exp.wantPrompt), len(gotPrompt))
		}

		gotPlan := PlanPolicy(exp.realModel, false)
		if gotPlan != exp.wantPlan {
			t.Errorf("model %q (family %q) got unexpected plan policy; want %v (len %d), got len %d",
				exp.realModel, exp.matchKey, exp.wantPlan[:30], len(exp.wantPlan), len(gotPlan))
		}
	}
}

// routing.go:64 classifies "gpt-oss" alongside -12b, -14b, etc., in
// CategoryBalanced as an open-weight local model. In promptVariants and
// planVariants, however, the unanchored "gpt-" prefix previously matched
// first, handing it the frontier gptVariant with stopping rules.
//
// A local model needs localVariant and localPlanPolicy so it receives the
// concise single-fanout policy rather than the procedural stopping-rule
// preamble meant for proprietary frontier models.
func TestGptOssGetsTheLocalVariantRatherThanTheFrontierPrompt(t *testing.T) {
	for _, model := range []string{"gpt-oss", "gpt-oss-12b", "gpt-oss-20b"} {
		if got := OrchestrationPrompt(model, false); got != localVariant {
			t.Errorf("%q got frontier prompt variant, want localVariant", model)
		}
		if got := PlanPolicy(model, false); got != localPlanPolicy {
			t.Errorf("%q got frontier plan policy, want localPlanPolicy", model)
		}
	}
}
