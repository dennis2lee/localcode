package dictation

import "testing"

func TestJoinTokens(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		tokens  []string
		want    string
		rebuilt bool
	}{{
		// The measurement this whole thing exists for: captured from
		// LC_DICTATION_DEBUG on Windows with the shipped Korean model.
		// The tokens are spaced correctly and the text is not.
		name:    "korean tokens carry spaces the text lost",
		text:    "는구체적인돈을남겼어.",
		tokens:  []string{"는", " 구", "체", "적인", " 돈을", " 남", "겼", "어", "."},
		want:    "는 구체적인 돈을 남겼어.",
		rebuilt: true,
	}, {
		name:    "sentencepiece marks become spaces",
		tokens:  []string{"▁hello", "▁wor", "ld"},
		text:    "helloworld",
		want:    "hello world",
		rebuilt: true,
	}, {
		// A recognizer that already spaced its own text is left alone,
		// so fixing one model cannot quietly reshape another.
		name:   "text that already has spaces is kept",
		text:   "hello world",
		tokens: []string{"▁hello", "▁world"},
		want:   "hello world",
	}, {
		// Genuinely unspaced scripts must not gain spaces between every
		// character just because they lack boundary marks.
		name:   "no marks means no rebuild",
		text:   "今天天气很好",
		tokens: []string{"今", "天", "天", "气", "很", "好"},
		want:   "今天天气很好",
	}, {
		name:   "no tokens",
		text:   "whatever",
		tokens: nil,
		want:   "whatever",
	}, {
		name:    "leading mark does not leak a leading space",
		text:    "안녕하세요",
		tokens:  []string{" 안녕", "하세요"},
		want:    "안녕하세요",
		rebuilt: true,
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, rebuilt := joinTokens(tc.text, tc.tokens)
			if got != tc.want {
				t.Errorf("text = %q, want %q", got, tc.want)
			}
			if rebuilt != tc.rebuilt {
				t.Errorf("rebuilt = %v, want %v", rebuilt, tc.rebuilt)
			}
		})
	}
}

// The counter that reported "0 start a word" for tokens plainly carrying
// spaces, and so pointed the reading at the wrong fault.
func TestSummarizeCountsPlainSpacesAsWordMarks(t *testing.T) {
	marks, empty := summarize([]string{"는", " 구", "체", "적인", " 돈을", " 남"})
	if marks != 3 {
		t.Errorf("word marks = %d, want 3", marks)
	}
	if empty != 0 {
		t.Errorf("empty = %d, want 0", empty)
	}
}
