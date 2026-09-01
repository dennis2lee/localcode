package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// A thinking block has to go back to the model exactly as it arrived, and
// on the current Claude models its visible text is routinely empty.
//
// The `display` parameter defaults to omitting the reasoning text on
// Fable 5, Opus 5, Opus 4.8, Opus 4.7 and Sonnet 5: the thinking happens
// and is billed, and only the words are withheld. What arrives is a
// signed block whose text is "". `omitempty` dropped that field on the
// way back out, so the continuation carried
// {"type":"thinking","signature":"..."} and the API refused the whole
// request with
//
//	messages.5.content.0.thinking.thinking: Field required
//
// The first few turns worked because the first few had no thinking block
// to replay yet.
func TestAThinkingBlockKeepsItsFieldsWhenTheyAreEmpty(t *testing.T) {
	block := anthContentBlock{Type: "thinking", Thinking: "", Signature: "sig-abc"}

	data, err := json.Marshal(block)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["thinking"]; !ok {
		t.Errorf("no \"thinking\" key: %s", data)
	}
	if got["thinking"] != "" {
		t.Errorf("thinking = %v, want the empty string it arrived as", got["thinking"])
	}
	if got["signature"] != "sig-abc" {
		t.Errorf("signature = %v", got["signature"])
	}
}

// The same for the signature, which is the other half of the pair. It
// cannot currently be empty on a block that is sent — toAnthropicMessages
// drops unsigned blocks — but the shape must not depend on a guard in
// another function to be correct.
func TestAThinkingBlockKeepsAnEmptySignatureToo(t *testing.T) {
	data, err := json.Marshal(anthContentBlock{Type: "thinking"})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"thinking", "signature"} {
		if !strings.Contains(string(data), `"`+key+`"`) {
			t.Errorf("no %q key: %s", key, data)
		}
	}
}

// And the reason this needs a marshaller rather than dropping omitempty:
// one struct carries every block type, so an unconditional json:"thinking"
// would put "thinking":"" on text, tool_use and tool_result blocks — which
// the API rejects just as firmly, in the other direction.
func TestOtherBlockTypesCarryNoThinkingField(t *testing.T) {
	for _, block := range []anthContentBlock{
		{Type: "text", Text: "hello"},
		{Type: "tool_use", ID: "t1", Name: "bash", Input: json.RawMessage(`{}`)},
		{Type: "tool_result", ToolUseID: "t1", Content: "ok"},
	} {
		data, err := json.Marshal(block)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"thinking", "signature"} {
			if strings.Contains(string(data), `"`+key+`"`) {
				t.Errorf("a %s block carries %q: %s", block.Type, key, data)
			}
		}
	}
}

// A thinking block can be the last block of its message — it is the only
// one, when the model thought and then said nothing — so it is a place
// markConversationCache can land a breakpoint. The narrower wire shape
// must not lose it.
func TestAThinkingBlockStillCarriesItsCacheBreakpoint(t *testing.T) {
	data, err := json.Marshal(anthContentBlock{
		Type: "thinking", Signature: "sig", CacheControl: ephemeral(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"cache_control"`) {
		t.Errorf("the breakpoint is gone: %s", data)
	}
}

// End to end through the conversion that builds a request, since that is
// where the wrong bytes actually went out.
func TestAReplayedThinkingBlockWithNoTextIsAcceptableJSON(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: []Block{{Type: BlockText, Text: "hello"}}},
		{Role: RoleAssistant, Content: []Block{
			{Type: BlockThinking, Text: "", Signature: "sig-abc"},
			{Type: BlockText, Text: "hi"},
		}},
	}
	out := toAnthropicMessages(msgs)
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"thinking":""`) {
		t.Errorf("the replayed block lost its empty thinking text: %s", data)
	}
}
