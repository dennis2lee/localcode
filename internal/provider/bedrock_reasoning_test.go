package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// The SDK states the requirement on ReasoningTextBlock itself: "If you
// pass a reasoning block back to the API in a multi-turn conversation,
// include the text and its signature unmodified." So the block a
// continuation needs goes back whole — and no other one does, because
// that reasoning is spent and re-sending it costs tokens for nothing.
func TestBedrockSendsReasoningBackOnlyWhereTheAPINeedsIt(t *testing.T) {
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

	out, err := toBedrockMessages(msgs)
	if err != nil {
		t.Fatalf("toBedrockMessages: %v", err)
	}

	if got := reasoningIn(t, out[1]); got != nil {
		t.Errorf("a spent reasoning block was re-sent: %+v", got)
	}
	live := reasoningIn(t, out[3])
	if live == nil {
		t.Fatal("the live reasoning block was dropped, so a continuation would be refused")
	}
	if aws.ToString(live.Text) != "current reasoning" {
		t.Errorf("text = %q, want the current reasoning", aws.ToString(live.Text))
	}
	// Unmodified, in the SDK's own words: an altered signature is worse
	// than a missing one, because it claims to attest to text it does not.
	if aws.ToString(live.Signature) != "sig-now" {
		t.Errorf("signature = %q, want it passed through untouched", aws.ToString(live.Signature))
	}
	// And it comes first: the reasoning explains the tool call, and an
	// order that puts it afterwards is not the message the model sent.
	if _, first := out[3].Content[0].(*types.ContentBlockMemberReasoningContent); !first {
		t.Errorf("Content[0] = %T, want the reasoning block first", out[3].Content[0])
	}
}

func TestBedrockNeverSendsAnUnsignedReasoningBlock(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: []Block{TextBlock("hello")}},
		{Role: RoleAssistant, Content: []Block{
			{Type: BlockThinking, Text: "reconstructed"},
			TextBlock("hi"),
		}},
	}
	out, err := toBedrockMessages(msgs)
	if err != nil {
		t.Fatalf("toBedrockMessages: %v", err)
	}
	if got := reasoningIn(t, out[1]); got != nil {
		t.Errorf("an unsigned block was sent, claiming an attestation that does not exist: %+v", got)
	}
}

// reasoningIn returns the reasoning text block in a message, or nil.
func reasoningIn(t *testing.T, m types.Message) *types.ReasoningTextBlock {
	t.Helper()
	for _, b := range m.Content {
		rc, ok := b.(*types.ContentBlockMemberReasoningContent)
		if !ok {
			continue
		}
		rt, ok := rc.Value.(*types.ReasoningContentBlockMemberReasoningText)
		if !ok {
			t.Fatalf("reasoning content = %T, want reasoning text", rc.Value)
		}
		return &rt.Value
	}
	return nil
}

// The bug this shape forecloses: the field is one document, and two
// features wrote it. Whichever assigned last used to win, so asking for
// reasoning on a million-token model would have quietly dropped the beta
// header that makes it a million-token model.
func TestTheTwoAdditionalFieldsDoNotOverwriteEachOther(t *testing.T) {
	fields := bedrockExtraFields(true, "us.anthropic.claude-sonnet-4-5-20250929-v1:0", EffortHigh, 0)
	doc := document.NewLazyDocument(fields)
	got := unmarshalDocument(t, doc.(interface{ MarshalSmithyDocument() ([]byte, error) }))

	if _, ok := got["anthropic_beta"]; !ok {
		t.Errorf("the million-token beta was dropped by the reasoning field: %v", got)
	}
	th, ok := got[bedrockThinkingField].(map[string]any)
	if !ok {
		t.Fatalf("no %q in %v", bedrockThinkingField, got)
	}
	if th["type"] != "enabled" || th["budget_tokens"] == nil {
		t.Errorf("thinking = %v, want the budget shape for this model", th)
	}
}

// The safety property, on this adapter too: nothing is sent for anybody
// who has not asked, so a request is byte for byte what it was.
func TestBedrockSendsNoExtraFieldsWhenNobodyAsked(t *testing.T) {
	if got := bedrockExtraFields(false, "us.anthropic.claude-opus-4-6-v1", EffortUnset, 0); len(got) != 0 {
		t.Errorf("unset effort produced %v, want nothing", got)
	}
	if got := bedrockExtraFields(false, "us.anthropic.claude-opus-4-6-v1", EffortOff, 0); len(got) != 0 {
		t.Errorf("off produced %v, want nothing", got)
	}
}

// A Bedrock model id carries the family inside a longer string
// ("us.anthropic.claude-opus-5-v1:0"), so the shape has to be chosen by
// what the id contains rather than by what it equals.
func TestBedrockPicksTheShapeFromAModelIDWithARegionAndAVersionOnIt(t *testing.T) {
	fields := bedrockExtraFields(false, "us.anthropic.claude-opus-5-v1:0", EffortLow, 0)
	th, ok := fields[bedrockThinkingField].(map[string]any)
	if !ok {
		t.Fatalf("thinking = %T, want a map", fields[bedrockThinkingField])
	}
	if th["type"] != "adaptive" {
		t.Errorf("type = %q, want adaptive for a family that rejects a budget", th["type"])
	}
}

// The one part of this that cannot be verified from here is which
// spelling an account accepts. A rejection therefore has to explain
// itself: an unexplained ValidationException naming a field nobody set on
// purpose is the worst version of being wrong about it.
func TestARejectedReasoningParameterExplainsItself(t *testing.T) {
	err := errors.New("operation error Bedrock Runtime: ConverseStream, ValidationException: " +
		"malformed input request: #/thinking: extraneous key is not permitted")

	got := reasoningRejected(err, EffortHigh)
	for _, want := range []string{"/effort off", bedrockThinkingField, "high"} {
		if !strings.Contains(got.Error(), want) {
			t.Errorf("hint %q does not mention %q", got, want)
		}
	}
	if !errors.Is(got, err) {
		t.Error("the original error was replaced rather than wrapped")
	}
}

func TestAnUnrelatedBedrockErrorIsLeftAlone(t *testing.T) {
	err := errors.New("operation error Bedrock Runtime: ConverseStream, ThrottlingException: slow down")
	if got := reasoningRejected(err, EffortHigh); got.Error() != err.Error() {
		t.Errorf("a throttle was decorated with a reasoning hint: %v", got)
	}
	// And nothing is added when nobody asked for reasoning, whatever the
	// error says: the hint would be advice about a setting that is off.
	validation := errors.New("ValidationException: #/thinking: extraneous key is not permitted")
	if got := reasoningRejected(validation, EffortUnset); got.Error() != validation.Error() {
		t.Errorf("an unset session was told to turn reasoning off: %v", got)
	}
}

// A stream's reasoning arrives as text and signature separately, in
// pieces, and the signature comes after the text it signs — so neither
// half is complete until the block closes.
func TestReasoningDeltasAccumulateIntoOneSignedBlock(t *testing.T) {
	var text, signature strings.Builder
	deltas := []types.ReasoningContentBlockDelta{
		&types.ReasoningContentBlockDeltaMemberText{Value: "first half "},
		&types.ReasoningContentBlockDeltaMemberText{Value: "second half"},
		&types.ReasoningContentBlockDeltaMemberSignature{Value: "sig-"},
		&types.ReasoningContentBlockDeltaMemberSignature{Value: "abc"},
		// Encrypted by the provider: not showable, and not the block a
		// continuation returns. It must land in neither half.
		&types.ReasoningContentBlockDeltaMemberRedactedContent{Value: []byte("opaque")},
	}
	for _, d := range deltas {
		t2, s2 := reasoningDelta(d)
		text.WriteString(t2)
		signature.WriteString(s2)
	}
	if text.String() != "first half second half" {
		t.Errorf("text = %q", text.String())
	}
	if signature.String() != "sig-abc" {
		t.Errorf("signature = %q", signature.String())
	}
}

// The value that goes on the wire is the same object the direct Anthropic
// adapter builds. One definition of what extended thinking looks like,
// rather than two tables that can disagree about a budget.
func TestBothAdaptersAskForTheSameThing(t *testing.T) {
	const model = "claude-sonnet-4-5-20250929"
	// Compared as decoded values, not as bytes: a struct and a map
	// serialize their keys in different orders and that difference is not
	// one the wire can see.
	raw, err := json.Marshal(anthropicThinking(model, EffortMedium, 0))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var direct map[string]any
	if err := json.Unmarshal(raw, &direct); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	viaBedrock, _ := bedrockExtraFields(false, model, EffortMedium, 0)[bedrockThinkingField].(map[string]any)
	if fmt.Sprint(direct) != fmt.Sprint(viaBedrock) {
		t.Errorf("bedrock asks for %v and the direct API asks for %v", viaBedrock, direct)
	}
	// And the keys are the wire's, not Go's: a smithy document serializes
	// a struct by its field names, so this is the check that catches
	// {"Type":"enabled","BudgetTokens":8192} going out.
	if _, ok := viaBedrock["budget_tokens"]; !ok {
		t.Errorf("bedrock's thinking has Go field names rather than wire keys: %v", viaBedrock)
	}
}

// capturingBedrockClient records the input it was handed and refuses, so
// a test can assert on the whole request the adapter built rather than on
// the pieces it built the request from.
type capturingBedrockClient struct {
	input *bedrockruntime.ConverseStreamInput
}

func (c *capturingBedrockClient) ConverseStream(
	_ context.Context, in *bedrockruntime.ConverseStreamInput, _ ...func(*bedrockruntime.Options),
) (*bedrockruntime.ConverseStreamOutput, error) {
	c.input = in
	return nil, errors.New("captured")
}

func captureBedrockRequest(t *testing.T, req ChatRequest) *bedrockruntime.ConverseStreamInput {
	t.Helper()
	fake := &capturingBedrockClient{}
	p := NewBedrock("us-east-1", "")
	p.load = func(context.Context, string, string) (bedrockClient, error) { return fake, nil }
	if _, err := p.Chat(context.Background(), req); err == nil {
		t.Fatal("expected the capturing client's refusal")
	}
	if fake.input == nil {
		t.Fatal("the adapter never reached the client")
	}
	return fake.input
}

// The whole request, not the pieces: this is what would actually go to
// Bedrock, and it is where a mistake in wiring the pieces together shows.
func TestTheWholeBedrockRequestCarriesReasoningAndNoTemperature(t *testing.T) {
	in := captureBedrockRequest(t, ChatRequest{
		Model:       "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		MaxTokens:   32000,
		Temperature: 0.3,
		Effort:      EffortMedium,
		Messages:    []Message{{Role: RoleUser, Content: []Block{TextBlock("hello")}}},
	})

	if in.AdditionalModelRequestFields == nil {
		t.Fatal("no additional model request fields, so nothing asked for reasoning")
	}
	got := unmarshalDocument(t, in.AdditionalModelRequestFields.(interface {
		MarshalSmithyDocument() ([]byte, error)
	}))
	th, ok := got[bedrockThinkingField].(map[string]any)
	if !ok {
		t.Fatalf("no %q in %v", bedrockThinkingField, got)
	}
	if th["type"] != "enabled" || th["budget_tokens"] == nil {
		t.Errorf("thinking = %v, want the budget shape", th)
	}
	if in.InferenceConfig == nil || in.InferenceConfig.Temperature != nil {
		t.Errorf("a reasoning request carried a temperature, which the API refuses alongside thinking")
	}
}

// And the same request without a level is the request it always was.
func TestTheWholeBedrockRequestIsUnchangedWhenNobodyAsked(t *testing.T) {
	in := captureBedrockRequest(t, ChatRequest{
		Model:       "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		MaxTokens:   32000,
		Temperature: 0.3,
		Messages:    []Message{{Role: RoleUser, Content: []Block{TextBlock("hello")}}},
	})
	if in.AdditionalModelRequestFields != nil {
		t.Errorf("an unconfigured request carried additional model fields")
	}
	if in.InferenceConfig == nil || in.InferenceConfig.Temperature == nil {
		t.Error("an ordinary request lost its temperature")
	}
}
