// Package modelinfo provides a best-effort lookup of a model's max context
// window size, used only for the UI's "context window used" indicator —
// not for anything billing- or correctness-critical, so an approximate
// match against known model families with a conservative fallback is
// good enough.
package modelinfo

import "strings"

// DefaultMaxContextTokens is used when modelID doesn't match any known
// family — a conservative, widely-supported figure.
const DefaultMaxContextTokens = 128000

// entry pairs a substring to match against a model ID (case-insensitively)
// with that family's context window size. Order matters: first match
// wins, so more specific substrings should precede more general ones.
type entry struct {
	substr string
	tokens int
}

var knownFamilies = []entry{
	// Claude (Bedrock model IDs carry a region prefix like "us."/"global."
	// and an "anthropic." vendor prefix; direct Anthropic API IDs don't —
	// matching on the family substring handles both).
	{"claude-opus", 200000},
	{"claude-sonnet", 200000},
	{"claude-haiku", 200000},
	{"claude-fable", 200000},

	// OpenAI (in case an openai-compat endpoint proxies to real OpenAI
	// models under their own names).
	{"gpt-4o", 128000},
	{"gpt-4-turbo", 128000},
	{"gpt-4", 8192},
	{"gpt-3.5", 16385},

	// Common local/open models served via LM Studio/vLLM.
	{"qwen3", 32768},
	{"qwen2.5", 32768},
	{"llama-3.1", 128000},
	{"llama-3.2", 128000},
	{"llama-3", 8192},
	{"mixtral", 32768},
}

// MaxContextTokens returns modelID's known context window size, or
// DefaultMaxContextTokens if it doesn't match any known family.
func MaxContextTokens(modelID string) int {
	lower := strings.ToLower(modelID)
	for _, e := range knownFamilies {
		if strings.Contains(lower, e.substr) {
			return e.tokens
		}
	}
	return DefaultMaxContextTokens
}
