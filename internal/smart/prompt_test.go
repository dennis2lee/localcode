package smart

import (
	"strings"
	"testing"
)

func TestEveryModelGetsAnOrchestrationPrompt(t *testing.T) {
	for _, model := range []string{"claude-opus-5", "gpt-5", "gemini-3-pro", "qwen3-coder-30b", "something-nobody-has-heard-of", ""} {
		if OrchestrationPrompt(model) == "" {
			t.Errorf("%q got no prompt", model)
		}
	}
}

// The small local models get the short flat version. The long procedural
// one competes with the task itself for attention on a 30B, and what
// survives is the first line and the last.
func TestLocalModelsGetTheShortVariant(t *testing.T) {
	long := len(OrchestrationPrompt("claude-opus-5"))
	for _, model := range []string{"qwen3-coder-30b", "glm-4-6", "muse-glimmer-30b", "llama-3.3-70b", "gemma-3-27b-it-q4"} {
		got := OrchestrationPrompt(model)
		if len(got) >= long {
			t.Errorf("%q got a prompt as long as the default one", model)
		}
		if strings.Contains(got, "TaskBackground") {
			t.Errorf("%q was told about background delegation; the local variant deliberately leaves it out", model)
		}
	}
}

// An unrecognised id is assumed capable rather than assumed small: the
// base prompt is the one written for a model that follows a stated policy,
// and nothing about an unknown name says it does not.
func TestAnUnknownModelGetsTheBasePrompt(t *testing.T) {
	if OrchestrationPrompt("who-knows-7") != baseOrchestration {
		t.Error("an unknown model did not get the base prompt")
	}
}

// Every variant has to actually name the tools it tells the model to use,
// or it is describing a capability the model was not given.
func TestEveryVariantNamesTheTaskTool(t *testing.T) {
	for _, model := range []string{"claude-opus-5", "gpt-5", "gemini-3-pro", "qwen3-coder-30b"} {
		if !strings.Contains(OrchestrationPrompt(model), "Task") {
			t.Errorf("%q was never told what to delegate with", model)
		}
	}
}
