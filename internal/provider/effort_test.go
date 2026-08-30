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
	if th := anthropicThinking("claude-opus-5", EffortUnset, 0); th != nil {
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
	if th := anthropicThinking("claude-opus-5", EffortOff, 0); th != nil {
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
		th := anthropicThinking(model, EffortHigh, 0)
		if th == nil || th.Type != "adaptive" {
			t.Errorf("%s asked for %+v, want adaptive thinking", model, th)
			continue
		}
		if th.BudgetTokens != 0 {
			t.Errorf("%s was sent a budget of %d, which that family rejects", model, th.BudgetTokens)
		}
	}

	for _, model := range []string{"claude-3-5-sonnet-20241022", "claude-opus-4-20250514"} {
		th := anthropicThinking(model, EffortHigh, 0)
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
		th := anthropicThinking("claude-3-5-sonnet-20241022", level, 0)
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

// The budget is spent out of max_tokens, so a budget larger than the cap
// is refused outright and one equal to it leaves nothing to answer with.
// This is not an exotic pairing: the shipped example config puts
// max_tokens 8192 on the profiles these levels apply to, and "high" is
// 16384 — so without this, turning effort up on that profile is a 400
// rather than more thinking.
func TestABudgetIsFittedToTheOutputCap(t *testing.T) {
	th := anthropicThinking("claude-3-5-sonnet-20241022", EffortHigh, 8192)
	if th == nil {
		t.Fatal("no thinking was asked for at all")
	}
	if th.BudgetTokens >= 8192 {
		t.Errorf("budget %d is not smaller than the 8192 cap it is spent from", th.BudgetTokens)
	}
	if th.BudgetTokens <= 0 {
		t.Errorf("budget = %d, want what the cap can pay for", th.BudgetTokens)
	}

	// A cap too small for any useful reasoning asks for none, rather than
	// asking for a reasoning model with no room to reason.
	if th := anthropicThinking("claude-3-5-sonnet-20241022", EffortHigh, 1200); th != nil {
		t.Errorf("a 1200-token cap still asked for %+v", th)
	}

	// An unstated cap is nothing to fit against, and the level stands.
	if th := anthropicThinking("claude-3-5-sonnet-20241022", EffortLow, 0); th == nil || th.BudgetTokens != 2048 {
		t.Errorf("with no cap the level's own budget should stand, got %+v", th)
	}
}

// Temperature and extended thinking do not go together: the API fixes the
// temperature while a model is reasoning and refuses a request that also
// sets one. Dropping it is what leaves both features usable — the
// alternative is that a profile with a temperature cannot ask for
// reasoning at all.
func TestTemperatureIsDroppedWhenReasoningIsAskedFor(t *testing.T) {
	reasoning := ChatRequest{Model: "claude-opus-5", MaxTokens: 4096, Temperature: 0.2, Effort: EffortHigh}
	if got := temperatureFor(reasoning); got != 0 {
		t.Errorf("a reasoning request carries temperature %v, which the API refuses alongside thinking", got)
	}

	plain := ChatRequest{Model: "claude-opus-5", MaxTokens: 4096, Temperature: 0.2}
	if got := temperatureFor(plain); got != 0.2 {
		t.Errorf("an ordinary request lost its temperature: %v", got)
	}

	// And a request whose cap is too small for a budget is not a
	// reasoning request, so it keeps its temperature.
	tooSmall := ChatRequest{
		Model: "claude-3-5-sonnet-20241022", MaxTokens: 1200, Temperature: 0.2, Effort: EffortHigh,
	}
	if got := temperatureFor(tooSmall); got != 0.2 {
		t.Errorf("a request that ended up asking for no thinking still dropped its temperature: %v", got)
	}
}

// Both adapters follow the same rule from the same function, so they
// cannot drift into disagreeing about it.
func TestTheTemperatureRuleIsOneRule(t *testing.T) {
	req := ChatRequest{Model: "us.anthropic.claude-sonnet-4-5-20250929-v1:0", MaxTokens: 8192, Temperature: 0.7, Effort: EffortLow}
	if got := temperatureFor(req); got != 0 {
		t.Errorf("a Bedrock reasoning request carries temperature %v", got)
	}
	cfg := buildInferenceConfig(req.MaxTokens, temperatureFor(req))
	if cfg.Temperature != nil {
		t.Errorf("the inference config still carries a temperature: %v", *cfg.Temperature)
	}
}
