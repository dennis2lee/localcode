package provider

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

func TestWrapCredentialErrorAddsHintForIMDSFallback(t *testing.T) {
	original := errors.New("bedrock ConverseStream: operation error Bedrock Runtime: ConverseStream, exceeded maximum number of attempts, 3, get identity: get credentials: failed to refresh cached credentials, no EC2 IMDS role found, operation error ec2imds: GetMetadata, exceeded maximum number of attempts, 3, request send failed")

	wrapped := wrapCredentialError(original)

	if !strings.Contains(wrapped.Error(), "hint:") {
		t.Errorf("wrapped error = %q, want it to contain an actionable hint", wrapped.Error())
	}
	if !strings.Contains(wrapped.Error(), "providers.<name>.profile") {
		t.Errorf("wrapped error = %q, want it to mention setting providers.<name>.profile", wrapped.Error())
	}
	if !strings.Contains(wrapped.Error(), original.Error()) {
		t.Errorf("wrapped error = %q, want the original error text preserved", wrapped.Error())
	}
}

func TestWrapCredentialErrorLeavesUnrelatedErrorsAlone(t *testing.T) {
	original := errors.New("bedrock ConverseStream: model not found")
	if wrapped := wrapCredentialError(original); wrapped.Error() != original.Error() {
		t.Errorf("wrapped error = %q, want unrelated errors passed through unchanged", wrapped.Error())
	}
}

func TestWrapCredentialErrorNilIsNil(t *testing.T) {
	if wrapCredentialError(nil) != nil {
		t.Error("wrapCredentialError(nil) should return nil")
	}
}

// These tests exercise only the pure translation functions (block model
// <-> Bedrock SDK types); none of them touch the network or need AWS
// credentials, so they run anywhere.

func TestToBedrockMessagesText(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: []Block{TextBlock("hello")}},
		{Role: RoleAssistant, Content: []Block{TextBlock("hi there")}},
	}

	out, err := toBedrockMessages(msgs)
	if err != nil {
		t.Fatalf("toBedrockMessages: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(out))
	}

	if out[0].Role != types.ConversationRoleUser {
		t.Errorf("msg[0].Role = %v, want user", out[0].Role)
	}
	if out[1].Role != types.ConversationRoleAssistant {
		t.Errorf("msg[1].Role = %v, want assistant", out[1].Role)
	}

	text0, ok := out[0].Content[0].(*types.ContentBlockMemberText)
	if !ok {
		t.Fatalf("msg[0].Content[0] = %T, want *ContentBlockMemberText", out[0].Content[0])
	}
	if text0.Value != "hello" {
		t.Errorf("msg[0] text = %q, want %q", text0.Value, "hello")
	}
}

func TestToBedrockMessagesToolUse(t *testing.T) {
	input, _ := json.Marshal(map[string]any{"pattern": "*.go"})
	msgs := []Message{
		{Role: RoleAssistant, Content: []Block{{
			Type:      BlockToolUse,
			ToolUseID: "call_1",
			ToolName:  "glob",
			ToolInput: input,
		}}},
	}

	out, err := toBedrockMessages(msgs)
	if err != nil {
		t.Fatalf("toBedrockMessages: %v", err)
	}

	block, ok := out[0].Content[0].(*types.ContentBlockMemberToolUse)
	if !ok {
		t.Fatalf("Content[0] = %T, want *ContentBlockMemberToolUse", out[0].Content[0])
	}
	if aws.ToString(block.Value.ToolUseId) != "call_1" {
		t.Errorf("ToolUseId = %q, want %q", aws.ToString(block.Value.ToolUseId), "call_1")
	}
	if aws.ToString(block.Value.Name) != "glob" {
		t.Errorf("Name = %q, want %q", aws.ToString(block.Value.Name), "glob")
	}

	decoded := unmarshalDocument(t, block.Value.Input)
	if decoded["pattern"] != "*.go" {
		t.Errorf("decoded input pattern = %v, want %q", decoded["pattern"], "*.go")
	}
}

// unmarshalDocument reads back a document.Interface built by
// document.NewLazyDocument via its MarshalSmithyDocument + plain
// encoding/json, sidestepping a bug in this SDK version's
// UnmarshalSmithyDocument (it errors with "unsupported json type" on
// perfectly well-formed documents).
func unmarshalDocument(t *testing.T, d interface{ MarshalSmithyDocument() ([]byte, error) }) map[string]any {
	t.Helper()
	raw, err := d.MarshalSmithyDocument()
	if err != nil {
		t.Fatalf("MarshalSmithyDocument: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("json.Unmarshal document bytes: %v", err)
	}
	return v
}

func TestToBedrockMessagesToolResult(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: []Block{ToolResultBlock("call_1", "file1.go\nfile2.go", false)}},
		{Role: RoleUser, Content: []Block{ToolResultBlock("call_2", "boom", true)}},
	}

	out, err := toBedrockMessages(msgs)
	if err != nil {
		t.Fatalf("toBedrockMessages: %v", err)
	}

	ok1, ok := out[0].Content[0].(*types.ContentBlockMemberToolResult)
	if !ok {
		t.Fatalf("Content[0] = %T, want *ContentBlockMemberToolResult", out[0].Content[0])
	}
	if ok1.Value.Status != types.ToolResultStatusSuccess {
		t.Errorf("status = %v, want success", ok1.Value.Status)
	}
	text, ok := ok1.Value.Content[0].(*types.ToolResultContentBlockMemberText)
	if !ok || text.Value != "file1.go\nfile2.go" {
		t.Errorf("unexpected content: %+v", ok1.Value.Content[0])
	}

	err1, ok := out[1].Content[0].(*types.ContentBlockMemberToolResult)
	if !ok {
		t.Fatalf("Content[0] = %T, want *ContentBlockMemberToolResult", out[1].Content[0])
	}
	if err1.Value.Status != types.ToolResultStatusError {
		t.Errorf("status = %v, want error", err1.Value.Status)
	}
}

func TestToBedrockMessagesInvalidToolInput(t *testing.T) {
	msgs := []Message{
		{Role: RoleAssistant, Content: []Block{{
			Type:      BlockToolUse,
			ToolUseID: "call_1",
			ToolName:  "glob",
			ToolInput: json.RawMessage(`{not valid json`),
		}}},
	}
	if _, err := toBedrockMessages(msgs); err == nil {
		t.Fatal("expected an error for invalid tool_use input JSON")
	}
}

func TestToBedrockTools(t *testing.T) {
	tools := []Tool{
		{
			Name:        "glob",
			Description: "list files",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"}}}`),
		},
	}

	cfg, err := toBedrockTools(tools)
	if err != nil {
		t.Fatalf("toBedrockTools: %v", err)
	}
	if cfg == nil || len(cfg.Tools) != 1 {
		t.Fatalf("expected 1 tool spec, got %+v", cfg)
	}

	spec, ok := cfg.Tools[0].(*types.ToolMemberToolSpec)
	if !ok {
		t.Fatalf("Tools[0] = %T, want *ToolMemberToolSpec", cfg.Tools[0])
	}
	if aws.ToString(spec.Value.Name) != "glob" {
		t.Errorf("Name = %q, want %q", aws.ToString(spec.Value.Name), "glob")
	}

	schemaMember, ok := spec.Value.InputSchema.(*types.ToolInputSchemaMemberJson)
	if !ok {
		t.Fatalf("InputSchema = %T, want *ToolInputSchemaMemberJson", spec.Value.InputSchema)
	}
	decoded := unmarshalDocument(t, schemaMember.Value)
	if decoded["type"] != "object" {
		t.Errorf("decoded schema type = %v, want %q", decoded["type"], "object")
	}
}

func TestToBedrockToolsEmpty(t *testing.T) {
	cfg, err := toBedrockTools(nil)
	if err != nil {
		t.Fatalf("toBedrockTools(nil): %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil ToolConfiguration for no tools, got %+v", cfg)
	}
}

func TestMapBedrockStopReason(t *testing.T) {
	cases := []struct {
		in   types.StopReason
		want string
	}{
		{types.StopReasonToolUse, "tool_use"},
		{types.StopReasonMaxTokens, "max_tokens"},
		{types.StopReasonEndTurn, "end_turn"},
		{types.StopReasonStopSequence, "end_turn"}, // anything else falls back to end_turn
	}
	for _, c := range cases {
		if got := mapBedrockStopReason(c.in); got != c.want {
			t.Errorf("mapBedrockStopReason(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
