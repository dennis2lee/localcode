package provider

import (
	"strings"
	"testing"
)

// Two text blocks in one message are two things somebody said, and the
// OpenAI wire format has one string to say them in. What went between
// them was nothing at all.
//
// The shape this produced is the one a compaction leaves behind. The
// summary replaces the history as a single user message, so the next
// prompt is a second user message; sendableHistory merges them to keep
// the alternation every provider wants, and this flattened the two
// blocks into "...they discussed three files.fourth question, after the
// compaction". The question the person actually asked arrived glued to
// the end of a sentence about something else, which is what "/compact
// broke the next answer" looks like from the outside.
func TestTwoTextBlocksAreNotRunTogether(t *testing.T) {
	msgs := []Message{{
		Role: RoleUser,
		Content: []Block{
			TextBlock("SUMMARY: they discussed three files."),
			TextBlock("fourth question, after the compaction"),
		},
	}}

	out := toOpenAIMessages("", msgs)
	if len(out) != 1 {
		t.Fatalf("got %d messages, want 1", len(out))
	}
	content := out[0].Content
	if strings.Contains(content, "files.fourth") {
		t.Errorf("the two blocks were run together:\n%s", content)
	}
	if !strings.Contains(content, "SUMMARY: they discussed three files.\n\nfourth question, after the compaction") {
		t.Errorf("the blocks are not separated by a blank line:\n%q", content)
	}
}

// The same in an assistant message, which flattens the same way.
func TestTwoAssistantTextBlocksAreNotRunTogether(t *testing.T) {
	out := toOpenAIMessages("", []Message{{
		Role:    RoleAssistant,
		Content: []Block{TextBlock("first half."), TextBlock("second half.")},
	}})
	content := out[0].Content
	if strings.Contains(content, "half.second") {
		t.Errorf("the two blocks were run together:\n%s", content)
	}
}

// An empty block is not a paragraph break. A message whose only text is
// one block must be byte-identical to what it always was, which is every
// ordinary turn.
func TestOneTextBlockIsUnchanged(t *testing.T) {
	out := toOpenAIMessages("", []Message{
		{Role: RoleUser, Content: []Block{TextBlock("what is 2+2")}},
		{Role: RoleUser, Content: []Block{TextBlock(""), TextBlock("still one paragraph")}},
	})
	if got := out[0].Content; got != "what is 2+2" {
		t.Errorf("a single block came out as %q", got)
	}
	if got := out[1].Content; got != "still one paragraph" {
		t.Errorf("an empty block added a separator: %q", got)
	}
}
