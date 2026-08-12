package modelinfo

import "testing"

func TestMaxContextTokensKnownFamilies(t *testing.T) {
	cases := map[string]int{
		"us.anthropic.claude-opus-4-6-v1":                 1000000,
		"anthropic.claude-sonnet-4-5-20250929-v1:0":       200000,
		"global.anthropic.claude-haiku-4-5-20251001-v1:0": 200000,
		"claude-opus-4-8":                                 1000000,
		"claude-sonnet-5":                                 1000000,
		"gpt-4o":                                          128000,
		"gpt-4o-mini":                                     128000,
		"gpt-4-turbo":                                     128000,
		"gpt-4":                                           8192,
		"gpt-3.5-turbo":                                   16385,
		"qwen3-30b-a3b":                                   32768,
		"llama-3.1-70b":                                   128000,
		"mixtral-8x7b":                                    32768,
	}
	for model, want := range cases {
		if got := MaxContextTokens(model); got != want {
			t.Errorf("MaxContextTokens(%q) = %d, want %d", model, got, want)
		}
	}
}

func TestMaxContextTokensUnknownFallsBackToDefault(t *testing.T) {
	if got := MaxContextTokens("some-totally-unknown-model"); got != DefaultMaxContextTokens {
		t.Errorf("MaxContextTokens() = %d, want default %d", got, DefaultMaxContextTokens)
	}
}

func TestMaxContextTokensCaseInsensitive(t *testing.T) {
	if got := MaxContextTokens("Claude-Opus-4-6-V1"); got != 1000000 {
		t.Errorf("MaxContextTokens() = %d, want case-insensitive match (1000000)", got)
	}
}

// The million-token models. Getting these wrong is not cosmetic: the meter
// drives automatic compaction at 80%, so a 1M window reported as 200k
// summarizes the conversation a fifth of the way in — over and over,
// long before the real limit is anywhere near.
func TestMillionTokenModels(t *testing.T) {
	for _, id := range []string{
		"claude-opus-5",
		"claude-sonnet-5",
		"claude-opus-4-8",
		"claude-opus-4-7",
		"claude-opus-4-6",
		"claude-sonnet-4-6",
		"claude-fable-5",
		"claude-mythos-5",
		// The same models through Bedrock, which prefixes the id.
		"anthropic.claude-opus-5",
		"us.anthropic.claude-sonnet-5",
		"global.anthropic.claude-opus-4-8",
	} {
		if got := MaxContextTokens(id); got != 1000000 {
			t.Errorf("MaxContextTokens(%q) = %d, want 1000000", id, got)
		}
	}
}

// The 1M entries are matched before the family fallbacks, and must not
// swallow the models that really do have a 200k window.
func TestTwoHundredKModelsAreNotCaughtByTheMillionEntries(t *testing.T) {
	for _, id := range []string{
		"claude-opus-4-5",
		"claude-opus-4-1",
		"claude-sonnet-4-5",
		"claude-haiku-4-5",
		"claude-3-5-sonnet-20241022",
	} {
		if got := MaxContextTokens(id); got != 200000 {
			t.Errorf("MaxContextTokens(%q) = %d, want 200000", id, got)
		}
	}
}
