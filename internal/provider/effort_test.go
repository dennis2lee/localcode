package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// The one property that keeps this safe to ship: a request from anybody
// who has not asked for an effort level is byte-identical to what it was.
func TestAnUnsetEffortSendsNothing(t *testing.T) {
	body := oaRequest{Model: "muse-glimmer-30b", ReasoningEffort: openAIEffort(EffortUnset)}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(payload), "reasoning_effort") {
		t.Errorf("an unset effort put a field on the wire: %s", payload)
	}
	if th := anthropicThinking("claude-opus-5", EffortUnset); th != nil {
		t.Errorf("an unset effort asked for thinking: %+v", th)
	}
}

// Off is not a level. The field's vocabulary is low/medium/high, servers
// disagree about whether there is a word for none, and asking for one
// nobody agrees on is worse than saying nothing.
func TestOffIsNotSentAsALevel(t *testing.T) {
	if got := openAIEffort(EffortOff); got != "" {
		t.Errorf("openAIEffort(off) = %q, want the field omitted", got)
	}
	if th := anthropicThinking("claude-opus-5", EffortOff); th != nil {
		t.Errorf("anthropicThinking(off) = %+v, want nothing", th)
	}
}

func TestOpenAICompatCarriesTheLevel(t *testing.T) {
	for _, level := range []Effort{EffortLow, EffortMedium, EffortHigh} {
		if got := openAIEffort(level); got != string(level) {
			t.Errorf("openAIEffort(%s) = %q, want %q", level, got, level)
		}
	}
}

// The newest Claude families decide the size of their own reasoning and
// reject a budget outright; the older ones require one. Sending the wrong
// shape is a 400, not a degraded answer, which is why this is chosen from
// the model id rather than sent one way and hoped for.
func TestAnthropicThinkingMatchesTheModelsAge(t *testing.T) {
	adaptive := []string{
		"claude-opus-5", "claude-sonnet-5", "claude-opus-4-8", "claude-opus-4-6",
		"claude-sonnet-4-6", "us.anthropic.claude-opus-5-v1:0",
	}
	for _, model := range adaptive {
		th := anthropicThinking(model, EffortHigh)
		if th == nil || th.Type != "adaptive" {
			t.Errorf("%s asked for %+v, want adaptive thinking", model, th)
			continue
		}
		if th.BudgetTokens != 0 {
			t.Errorf("%s was sent a budget of %d, which that family rejects", model, th.BudgetTokens)
		}
	}

	for _, model := range []string{"claude-3-5-sonnet-20241022", "claude-opus-4-20250514"} {
		th := anthropicThinking(model, EffortHigh)
		if th == nil || th.Type != "enabled" || th.BudgetTokens == 0 {
			t.Errorf("%s asked for %+v, want enabled thinking with a budget", model, th)
		}
	}
}

// On a model that takes a budget the three levels have to be three
// numbers, or the setting is a switch wearing a dial's clothes.
func TestTheLevelsAreThreeDifferentBudgets(t *testing.T) {
	seen := map[int]bool{}
	for _, level := range []Effort{EffortLow, EffortMedium, EffortHigh} {
		th := anthropicThinking("claude-3-5-sonnet-20241022", level)
		if th == nil {
			t.Fatalf("%s asked for no thinking", level)
		}
		if seen[th.BudgetTokens] {
			t.Errorf("%s reuses a budget another level already has (%d)", level, th.BudgetTokens)
		}
		seen[th.BudgetTokens] = true
	}
}

// A thinking block is only sent back where the API needs it: the last
// assistant message, which is the only one a continuation can continue.
// Every earlier one must be dropped — that reasoning is spent, the API
// does not want it, and re-sending pages of it costs tokens for nothing.
func TestOnlyTheLastAssistantMessageCarriesItsThinking(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: []Block{TextBlock("first")}},
		{Role: RoleAssistant, Content: []Block{
			{Type: BlockThinking, Text: "old reasoning", Signature: "sig-old"},
			TextBlock("an earlier answer"),
		}},
		{Role: RoleUser, Content: []Block{TextBlock("second")}},
		{Role: RoleAssistant, Content: []Block{
			{Type: BlockThinking, Text: "current reasoning", Signature: "sig-now"},
			{Type: BlockToolUse, ToolUseID: "t1", ToolName: "read_file"},
		}},
		{Role: RoleUser, Content: []Block{ToolResultBlock("t1", "contents", false)}},
	}

	out := toAnthropicMessages(msgs)
	payload, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(payload)
	if strings.Contains(body, "old reasoning") {
		t.Errorf("a spent thinking block was re-sent: %s", body)
	}
	if !strings.Contains(body, "current reasoning") || !strings.Contains(body, "sig-now") {
		t.Errorf("the live thinking block was dropped, so a continuation would be refused: %s", body)
	}
}

// An unsigned block is one this process assembled rather than received —
// a rehydrated history, a test. Sending it claims an attestation that
// does not exist, and the API refuses the request rather than the block.
func TestAnUnsignedThinkingBlockIsNotSent(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: []Block{TextBlock("hello")}},
		{Role: RoleAssistant, Content: []Block{
			{Type: BlockThinking, Text: "reconstructed"},
			TextBlock("hi"),
		}},
	}
	payload, err := json.Marshal(toAnthropicMessages(msgs))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(payload), "reconstructed") {
		t.Errorf("an unsigned thinking block was sent: %s", payload)
	}
}

func TestValidEffort(t *testing.T) {
	for _, ok := range []string{"off", "low", "medium", "high"} {
		if !ValidEffort(ok) {
			t.Errorf("%q was rejected", ok)
		}
	}
	for _, bad := range []string{"", "none", "max", "HIGH", "3"} {
		if ValidEffort(bad) {
			t.Errorf("%q was accepted", bad)
		}
	}
}
