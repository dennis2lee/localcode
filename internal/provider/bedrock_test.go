package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

type failingBedrockClient struct{}

func (failingBedrockClient) ConverseStream(context.Context, *bedrockruntime.ConverseStreamInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseStreamOutput, error) {
	return nil, errors.New("test request stopped")
}

func minimalBedrockRequest() ChatRequest {
	return ChatRequest{
		Model:     "anthropic.test",
		MaxTokens: 32,
		Messages:  []Message{{Role: RoleUser, Content: []Block{TextBlock("hello")}}},
	}
}

func TestBedrockDefersAWSConfigUntilFirstRequest(t *testing.T) {
	loads := 0
	p := NewBedrock("us-west-2", "missing-on-this-machine")
	p.load = func(context.Context, string, string) (bedrockClient, error) {
		loads++
		return nil, errors.New("load AWS config: missing profile")
	}

	if loads != 0 {
		t.Fatalf("constructing an unused Bedrock provider loaded AWS config %d times", loads)
	}
	if _, err := p.Chat(context.Background(), minimalBedrockRequest()); err == nil || !strings.Contains(err.Error(), "load AWS config") {
		t.Fatalf("first Bedrock request error = %v, want the deferred AWS config error", err)
	}
	if loads != 1 {
		t.Fatalf("first request loaded AWS config %d times, want 1", loads)
	}
}

func TestBedrockRetriesAConfigLoadThatFailed(t *testing.T) {
	loads := 0
	p := NewBedrock("us-west-2", "profile")
	p.load = func(context.Context, string, string) (bedrockClient, error) {
		loads++
		if loads == 1 {
			return nil, errors.New("temporary config error")
		}
		return failingBedrockClient{}, nil
	}

	if _, err := p.Chat(context.Background(), minimalBedrockRequest()); err == nil || err.Error() != "temporary config error" {
		t.Fatalf("first request error = %v, want temporary config error", err)
	}
	if _, err := p.Chat(context.Background(), minimalBedrockRequest()); err == nil || !strings.Contains(err.Error(), "test request stopped") {
		t.Fatalf("second request error = %v, want the fake client error", err)
	}
	if loads != 2 {
		t.Fatalf("AWS config loader called %d times, want one retry after failure", loads)
	}
}

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

	cfg, err := toBedrockTools(tools, false)
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

// Converse rejects the whole request when any tool's description is
// empty, and an MCP server may advertise a tool without one. One such tool
// at index 256 of 258 took every Opus turn down with a 400, so each tool
// here must come out with a description Converse accepts, and a tool that
// has one must keep it unchanged.
func TestToBedrockToolsNeverSendsAnEmptyDescription(t *testing.T) {
	tools := []Tool{
		{Name: "glob", Description: "list files"},
		{Name: "mcp__jira__get_issue", Description: ""},
		{Name: "mcp__jira__list_boards", Description: " \n\t"},
	}
	cfg, err := toBedrockTools(tools, true)
	if err != nil {
		t.Fatalf("toBedrockTools: %v", err)
	}
	want := []string{"list files", "mcp__jira__get_issue", "mcp__jira__list_boards"}
	specs := 0
	for _, tool := range cfg.Tools {
		spec, ok := tool.(*types.ToolMemberToolSpec)
		if !ok {
			continue
		}
		got := aws.ToString(spec.Value.Description)
		if got != want[specs] {
			t.Errorf("tool %s: description %q, want %q", aws.ToString(spec.Value.Name), got, want[specs])
		}
		specs++
	}
	if specs != len(tools) {
		t.Fatalf("got %d tool specs, want %d", specs, len(tools))
	}
}

// Converse takes tool names of 1 to 64 characters from [a-zA-Z0-9_-], and
// refuses the whole request over one that is not. MCP tools are named
// mcp__<server>__<tool>, so the prefix alone can push a name the server
// advertised legitimately over 64, and a server's name is its key in
// config.json, dots and spaces included. Every name must arrive in a form
// Converse takes, a name it already takes must arrive unchanged, and no
// two tools may arrive under one name.
func TestEveryToolNameBedrockIsSentFitsConverse(t *testing.T) {
	tools := []Tool{
		{Name: "glob", Description: "d"},
		{Name: "mcp__x__" + strings.Repeat("a", 60), Description: "d"},
		{Name: "mcp__x__" + strings.Repeat("a", 59) + "b", Description: "d"},
		{Name: "mcp__my.server__search", Description: "d"},
		{Name: "mcp__my server__search", Description: "d"},
		{Name: "mcp__my_server__search", Description: "d"},
	}
	cfg, err := toBedrockTools(tools, false)
	if err != nil {
		t.Fatalf("toBedrockTools: %v", err)
	}
	if len(cfg.Tools) != len(tools) {
		t.Fatalf("got %d tool specs, want %d", len(cfg.Tools), len(tools))
	}
	converse := regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	seen := map[string]string{}
	for i, tool := range cfg.Tools {
		name := aws.ToString(tool.(*types.ToolMemberToolSpec).Value.Name)
		if !converse.MatchString(name) {
			t.Errorf("%s was sent as %q, which Converse refuses", tools[i].Name, name)
		}
		if other, dup := seen[name]; dup {
			t.Errorf("%s and %s were both sent as %q", other, tools[i].Name, name)
		}
		seen[name] = tools[i].Name
	}
	for _, i := range []int{0, 5} {
		if got := aws.ToString(cfg.Tools[i].(*types.ToolMemberToolSpec).Value.Name); got != tools[i].Name {
			t.Errorf("%s was renamed to %q, though Converse takes it as it is", tools[i].Name, got)
		}
	}
}

// A tool sent under a substitute name is called by that name, and the
// call has to come back under the tool's own: that is the name the agent
// looks the tool up by. And a call from earlier in the conversation must
// be sent under the same substitute as the tool list, or the model reads
// its own history as calls to a tool it was never offered.
func TestAToolSentUnderASubstituteNameIsCalledUnderItsOwn(t *testing.T) {
	long := "mcp__jira__" + strings.Repeat("get_issue_with_every_field_", 3)
	req := minimalBedrockRequest()
	req.Tools = []Tool{{Name: long, Description: "d"}, {Name: "glob", Description: "d"}}
	req.Messages = []Message{
		{Role: RoleUser, Content: []Block{TextBlock("look it up")}},
		{Role: RoleAssistant, Content: []Block{{Type: BlockToolUse, ToolUseID: "t1", ToolName: long, ToolInput: json.RawMessage(`{}`)}}},
		{Role: RoleUser, Content: []Block{ToolResultBlock("t1", "found", false)}},
	}

	sent := captureBedrockRequest(t, req)
	offered := aws.ToString(sent.ToolConfig.Tools[0].(*types.ToolMemberToolSpec).Value.Name)
	if offered == long {
		t.Fatalf("precondition: %s (%d characters) was sent unchanged", long, len(long))
	}
	var inHistory string
	for _, m := range sent.Messages {
		for _, c := range m.Content {
			if tu, ok := c.(*types.ContentBlockMemberToolUse); ok {
				inHistory = aws.ToString(tu.Value.Name)
			}
		}
	}
	if inHistory != offered {
		t.Errorf("the earlier call is sent as %q, the tool is offered as %q", inHistory, offered)
	}

	events := make(chan types.ConverseStreamOutput, 2)
	for i, name := range []string{offered, "glob"} {
		events <- &types.ConverseStreamOutputMemberContentBlockStart{Value: types.ContentBlockStartEvent{
			ContentBlockIndex: aws.Int32(int32(i)),
			Start: &types.ContentBlockStartMemberToolUse{Value: types.ToolUseBlockStart{
				ToolUseId: aws.String(fmt.Sprintf("call%d", i)),
				Name:      aws.String(name),
			}},
		}}
	}
	close(events)
	out := make(chan StreamEvent, 8)
	streamBedrock(context.Background(), fakeBedrockStream{events}, req, out)
	var called []string
	for ev := range out {
		if ev.Type == EventToolUseStart {
			called = append(called, ev.ToolName)
		}
	}
	if len(called) != 2 || called[0] != long || called[1] != "glob" {
		t.Errorf("tool calls came back as %q, want [%q \"glob\"]", called, long)
	}
}

// fakeBedrockStream hands streamBedrock events the SDK has no way to fake:
// its output type keeps the stream in an unexported field.
type fakeBedrockStream struct {
	events chan types.ConverseStreamOutput
}

func (f fakeBedrockStream) Events() <-chan types.ConverseStreamOutput { return f.events }
func (fakeBedrockStream) Close() error                                { return nil }
func (fakeBedrockStream) Err() error                                  { return nil }

// Converse requires a tool's schema to be an object at the top level. A
// tool with no schema, a null one, or an object that does not say "type"
// all describe an object with no stated fields, and must arrive saying
// so; a schema that already says it must arrive as it was.
func TestEveryToolSchemaBedrockIsSentIsAnObject(t *testing.T) {
	tools := []Tool{
		{Name: "none", Description: "d"},
		{Name: "null", Description: "d", InputSchema: json.RawMessage(`null`)},
		{Name: "untyped", Description: "d", InputSchema: json.RawMessage(`{"properties":{"q":{"type":"string"}}}`)},
		{Name: "typed", Description: "d", InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`)},
	}
	cfg, err := toBedrockTools(tools, false)
	if err != nil {
		t.Fatalf("toBedrockTools: %v", err)
	}
	schemas := map[string]map[string]any{}
	for _, tool := range cfg.Tools {
		spec := tool.(*types.ToolMemberToolSpec).Value
		schemas[aws.ToString(spec.Name)] = unmarshalDocument(t, spec.InputSchema.(*types.ToolInputSchemaMemberJson).Value)
	}
	for _, tool := range tools {
		if got := schemas[tool.Name]["type"]; got != "object" {
			t.Errorf("%s: schema type %v, want object", tool.Name, got)
		}
	}
	if _, ok := schemas["untyped"]["properties"].(map[string]any)["q"]; !ok {
		t.Errorf("untyped: the stated fields were lost: %v", schemas["untyped"])
	}
	if req, _ := schemas["typed"]["required"].([]any); len(req) != 1 || req[0] != "q" {
		t.Errorf("typed: the schema was changed: %v", schemas["typed"])
	}
}

func TestToBedrockToolsEmpty(t *testing.T) {
	cfg, err := toBedrockTools(nil, false)
	if err != nil {
		t.Fatalf("toBedrockTools(nil): %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil ToolConfiguration for no tools, got %+v", cfg)
	}
}

func TestParseModelIDStripsOneMillionContextSuffix(t *testing.T) {
	cases := []struct {
		in       string
		wantID   string
		wantOneM bool
	}{
		{"us.anthropic.claude-sonnet-4-6[1m]", "us.anthropic.claude-sonnet-4-6", true},
		{"us.anthropic.claude-sonnet-4-6[1M]", "us.anthropic.claude-sonnet-4-6", true},  // case-insensitive
		{"us.anthropic.claude-sonnet-4-6 [1m]", "us.anthropic.claude-sonnet-4-6", true}, // tolerates a space before it
		{"us.anthropic.claude-sonnet-4-6", "us.anthropic.claude-sonnet-4-6", false},
		{"", "", false},
	}
	for _, c := range cases {
		gotID, gotOneM := parseModelID(c.in)
		if gotID != c.wantID || gotOneM != c.wantOneM {
			t.Errorf("parseModelID(%q) = (%q, %v), want (%q, %v)", c.in, gotID, gotOneM, c.wantID, c.wantOneM)
		}
	}
}

func TestBuildInferenceConfigOmitsZeroTemperature(t *testing.T) {
	cfg := buildInferenceConfig(4096, 0, nil)
	if cfg.Temperature != nil {
		t.Errorf("Temperature = %v, want nil when the profile never configured one (some models reject the field entirely at any value)", cfg.Temperature)
	}
	if aws.ToInt32(cfg.MaxTokens) != 4096 {
		t.Errorf("MaxTokens = %d, want 4096", aws.ToInt32(cfg.MaxTokens))
	}
}

func TestBuildInferenceConfigSetsExplicitTemperature(t *testing.T) {
	cfg := buildInferenceConfig(4096, 0.7, nil)
	if cfg.Temperature == nil {
		t.Fatal("Temperature = nil, want it set when the profile explicitly configured 0.7")
	}
	if got := aws.ToFloat32(cfg.Temperature); got != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", got)
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
