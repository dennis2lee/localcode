package modelinfo

import "testing"

// Claude 3.x IDs put the generation before the family
// ("claude-3-5-sonnet-...", "claude-3-opus-..."), so none of the family
// substrings reached them and they fell back to the default. The context
// meter then read about 1.6x the real usage — and auto-compaction fires
// off that number.
func TestClaude3IDsGetTheRealContextWindow(t *testing.T) {
	for _, id := range []string{
		"claude-3-5-sonnet-20241022",
		"claude-3-opus-20240229",
		"us.anthropic.claude-3-5-haiku-20241022-v1:0",
	} {
		if got := MaxContextTokens(id); got != 200000 {
			t.Errorf("MaxContextTokens(%q) = %d, want 200000", id, got)
		}
	}
}
