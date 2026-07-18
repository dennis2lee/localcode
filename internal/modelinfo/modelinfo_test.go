package modelinfo

import "testing"

func TestMaxContextTokensKnownFamilies(t *testing.T) {
	cases := map[string]int{
		"us.anthropic.claude-opus-4-6-v1":                 200000,
		"anthropic.claude-sonnet-4-5-20250929-v1:0":       200000,
		"global.anthropic.claude-haiku-4-5-20251001-v1:0": 200000,
		"claude-opus-4-8":                                 200000,
		"claude-sonnet-5":                                 200000,
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
	if got := MaxContextTokens("Claude-Opus-4-6-V1"); got != 200000 {
		t.Errorf("MaxContextTokens() = %d, want case-insensitive match (200000)", got)
	}
}
