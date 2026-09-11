package provider

import (
	"encoding/json"
	"testing"
)

// top_p and top_k on a profile, which is what muse's own recipe asks for
// alongside temperature 1.0 and what "/llm-doctor" could already set on
// its own request bodies while a profile could not.

func f64(v float64) *float64 { return &v }
func intp(v int) *int        { return &v }

// The OpenAI-compatible wire form. top_k is not in that schema — it is a
// vLLM extension — and is sent anyway when asked for, on the reasoning
// reasoning_effort is sent on.
func TestOpenAICarriesTheSamplingFamily(t *testing.T) {
	body := oaRequest{
		Model:       "muse-glimmer-30b",
		Temperature: 1.0,
		TopP:        f64(0.95),
		TopK:        intp(64),
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["top_p"] != 0.95 {
		t.Errorf("top_p = %v, want 0.95", got["top_p"])
	}
	if got["top_k"] != float64(64) {
		t.Errorf("top_k = %v, want 64", got["top_k"])
	}
}

// A profile that did not ask sends no field at all, which is what keeps
// this safe on every server written before it.
func TestOpenAISendsNothingWhenNothingWasAsked(t *testing.T) {
	raw, err := json.Marshal(oaRequest{Model: "m"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"top_p", "top_k"} {
		if _, present := got[key]; present {
			t.Errorf("%s was sent by a profile that never set it: %s", key, raw)
		}
	}
}

// Zero is a value here, not an absence: top_k 0 means "no limit" on
// vLLM, so a pointer at zero has to reach the wire.
func TestZeroIsSentRatherThanTreatedAsUnset(t *testing.T) {
	raw, err := json.Marshal(oaRequest{Model: "m", TopK: intp(0)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v, present := got["top_k"]; !present || v != float64(0) {
		t.Errorf("top_k 0 did not reach the wire: %s", raw)
	}
}

// While a Claude model is reasoning, the API decides how it samples and
// refuses a request that also says. top_k is not accepted with extended
// thinking at all and top_p only inside a narrow band, so both are
// dropped — which is what lets one profile carry a sampling recipe and
// ask for reasoning without the two colliding.
func TestSamplingIsDroppedWhileTheModelReasons(t *testing.T) {
	req := ChatRequest{
		Model:     "claude-opus-4-1-20250805",
		MaxTokens: 8192,
		Effort:    EffortHigh,
		TopP:      f64(0.95),
		TopK:      intp(64),
	}
	if anthropicThinking(req.Model, req.Effort, req.MaxTokens) == nil {
		t.Skip("this model does not take extended thinking, so there is nothing to drop")
	}
	topP, topK := samplingFor(req)
	if topP != nil || topK != nil {
		t.Errorf("samplingFor returned top_p=%v top_k=%v while reasoning, want both dropped", topP, topK)
	}

	// And with no reasoning asked for, both survive.
	req.Effort = EffortUnset
	topP, topK = samplingFor(req)
	if topP == nil || *topP != 0.95 {
		t.Errorf("top_p = %v, want 0.95 when nothing is reasoning", topP)
	}
	if topK == nil || *topK != 64 {
		t.Errorf("top_k = %v, want 64 when nothing is reasoning", topK)
	}
}

// Converse has no top_k of its own, so it travels as a native model
// parameter in the document extended thinking already uses.
func TestBedrockCarriesTopKInTheAdditionalFields(t *testing.T) {
	fields := bedrockExtraFields(false, "anthropic.claude-3-5-sonnet-20241022-v2:0", EffortUnset, 4096, intp(64))
	if fields["top_k"] != 64 {
		t.Errorf("top_k = %v, want 64", fields["top_k"])
	}

	// Nothing asked, nothing sent.
	if fields := bedrockExtraFields(false, "anthropic.claude-3-5-sonnet-20241022-v2:0", EffortUnset, 4096, nil); len(fields) != 0 {
		t.Errorf("fields = %v, want none for a profile that set nothing", fields)
	}
}

// top_p goes in the inference config, and only when asked for: the
// SDK's typed struct has no omitempty, which is the same trap the
// temperature check beside it exists for.
func TestBedrockTopPIsOnlySentWhenAsked(t *testing.T) {
	if cfg := buildInferenceConfig(4096, 0, nil); cfg.TopP != nil {
		t.Errorf("TopP = %v, want nothing sent for a profile that set none", *cfg.TopP)
	}
	cfg := buildInferenceConfig(4096, 0, f64(0.95))
	if cfg.TopP == nil || *cfg.TopP != 0.95 {
		t.Errorf("TopP = %v, want 0.95", cfg.TopP)
	}
}
