package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// Bedrock talks to Amazon Bedrock's Converse Stream API, which unifies tool
// use and streaming across Claude model versions on Bedrock. Auth is
// whatever the default AWS credential chain resolves (env vars, SSO cache,
// instance role, etc.) — no credentials are handled directly here.
type Bedrock struct {
	client *bedrockruntime.Client
}

// NewBedrock builds a Bedrock client for region. profile, if non-empty,
// selects a named AWS profile (e.g. one `localcode login bedrock` set up)
// via the shared config/credentials files instead of the default
// credential chain's usual resolution order.
func NewBedrock(ctx context.Context, region, profile string) (*Bedrock, error) {
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}
	if profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return &Bedrock{client: bedrockruntime.NewFromConfig(cfg)}, nil
}

// credentialHintSubstrings are fragments the AWS SDK's error text contains
// when it fell through the *entire* default credential chain (env vars,
// shared config, container role, EC2 IMDS) and found nothing — the
// exact symptom of "providers.bedrock.profile isn't set (or is wrong),
// and there's no AWS_PROFILE/default profile with working credentials
// either." This is easy to hit on Windows: a working `aws sso login` /
// `localcode login bedrock` profile does nothing unless something tells
// the SDK to actually use it.
var credentialHintSubstrings = []string{
	"no ec2imds role found",
	"failed to refresh cached credentials",
	"no valid credential sources",
	"unable to find credentials",
}

// wrapCredentialError appends an actionable hint to err when it looks like
// the AWS SDK never found any usable credentials, rather than leaving the
// user with only a raw multi-line SDK error dump (get identity: get
// credentials: failed to refresh cached credentials, no EC2 IMDS role
// found, operation error ec2imds: GetMetadata, ...).
func wrapCredentialError(err error) error {
	if err == nil {
		return nil
	}
	lower := strings.ToLower(err.Error())
	for _, s := range credentialHintSubstrings {
		if strings.Contains(lower, s) {
			return fmt.Errorf("%w\n\nhint: no AWS credentials were found. If you already ran `localcode login bedrock` or `aws sso login`, set providers.<name>.profile in config.json to that profile's name (localcode login bedrock defaults to \"localcode-bedrock\"), or set the AWS_PROFILE environment variable. If the SSO session expired, re-run the login command", err)
		}
	}
	return err
}

func toBedrockMessages(msgs []Message) ([]types.Message, error) {
	out := make([]types.Message, 0, len(msgs))
	for _, m := range msgs {
		role := types.ConversationRoleUser
		if m.Role == RoleAssistant {
			role = types.ConversationRoleAssistant
		}

		blocks := make([]types.ContentBlock, 0, len(m.Content))
		for _, b := range m.Content {
			switch b.Type {
			case BlockText:
				blocks = append(blocks, &types.ContentBlockMemberText{Value: b.Text})

			case BlockToolUse:
				var input any
				if len(b.ToolInput) > 0 {
					if err := json.Unmarshal(b.ToolInput, &input); err != nil {
						return nil, fmt.Errorf("unmarshal tool_use input for %s: %w", b.ToolName, err)
					}
				} else {
					input = map[string]any{}
				}
				blocks = append(blocks, &types.ContentBlockMemberToolUse{Value: types.ToolUseBlock{
					ToolUseId: aws.String(b.ToolUseID),
					Name:      aws.String(b.ToolName),
					Input:     document.NewLazyDocument(input),
				}})

			case BlockToolResult:
				status := types.ToolResultStatusSuccess
				if b.IsError {
					status = types.ToolResultStatusError
				}
				blocks = append(blocks, &types.ContentBlockMemberToolResult{Value: types.ToolResultBlock{
					ToolUseId: aws.String(b.ToolUseID),
					Status:    status,
					Content: []types.ToolResultContentBlock{
						&types.ToolResultContentBlockMemberText{Value: b.ToolResultContent},
					},
				}})
			}
		}

		out = append(out, types.Message{Role: role, Content: blocks})
	}
	return out, nil
}

func toBedrockTools(tools []Tool) (*types.ToolConfiguration, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	specs := make([]types.Tool, 0, len(tools))
	for _, t := range tools {
		var schema any
		if len(t.InputSchema) > 0 {
			if err := json.Unmarshal(t.InputSchema, &schema); err != nil {
				return nil, fmt.Errorf("unmarshal schema for tool %s: %w", t.Name, err)
			}
		} else {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		specs = append(specs, &types.ToolMemberToolSpec{Value: types.ToolSpecification{
			Name:        aws.String(t.Name),
			Description: aws.String(t.Description),
			InputSchema: &types.ToolInputSchemaMemberJson{Value: document.NewLazyDocument(schema)},
		}})
	}
	return &types.ToolConfiguration{Tools: specs}, nil
}

// oneMillionContextBeta is the Anthropic beta flag that unlocks the
// 1M-token context window on supported Claude Sonnet models. On direct
// Anthropic API calls this is sent as an "anthropic-beta" HTTP header;
// Bedrock's Converse API has no such header, so it's passed via
// AdditionalModelRequestFields instead (see parseModelID/Chat below).
const oneMillionContextBeta = "context-1m-2025-08-07"

// oneMillionContextSuffix is the "[1m]" marker Claude Code's own model
// config uses as shorthand for "enable the 1M-context beta on this
// model" — it's a convenience for humans configuring Claude Code, not
// part of the real Bedrock model ID, and sending it to the API as-is
// fails with "ValidationException: ... not authorized to invoke this
// API operation" (the ID simply doesn't exist). parseModelID recognizes
// the same shorthand so a config.json copied from Claude Code's settings
// (e.g. "us.anthropic.claude-sonnet-4-6[1m]") works as expected instead
// of silently failing.
const oneMillionContextSuffix = "[1m]"

// parseModelID splits a configured model string into the real model ID
// Bedrock expects and whether the "[1m]" 1M-context shorthand was
// present, case-insensitively and tolerant of surrounding whitespace.
func parseModelID(model string) (id string, oneMillionContext bool) {
	trimmed := strings.TrimSpace(model)
	if strings.HasSuffix(strings.ToLower(trimmed), oneMillionContextSuffix) {
		return strings.TrimSpace(trimmed[:len(trimmed)-len(oneMillionContextSuffix)]), true
	}
	return trimmed, false
}

// buildInferenceConfig only sets Temperature when temperature is
// non-zero — i.e. when the profile actually configured one in
// config.json. Some newer models (certain Opus versions among them)
// reject the field outright — "ValidationException: ... 'temperature'
// is deprecated for this model" — even at its zero value, which is what
// every profile that never set "temperature" sends by default if this
// were passed unconditionally. The OpenAI-compat and Anthropic-direct
// providers already dodge this for free via their wire structs'
// `omitempty` tag; the Bedrock SDK's typed InferenceConfiguration has no
// such tag, so it needs the same "don't send zero" check done explicitly.
func buildInferenceConfig(maxTokens int, temperature float64) *types.InferenceConfiguration {
	cfg := &types.InferenceConfiguration{MaxTokens: aws.Int32(int32(maxTokens))}
	if temperature != 0 {
		cfg.Temperature = aws.Float32(float32(temperature))
	}
	return cfg
}

func mapBedrockStopReason(r types.StopReason) string {
	switch r {
	case types.StopReasonToolUse:
		return "tool_use"
	case types.StopReasonMaxTokens:
		return "max_tokens"
	default:
		return "end_turn"
	}
}

func (p *Bedrock) Chat(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	messages, err := toBedrockMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	toolConfig, err := toBedrockTools(req.Tools)
	if err != nil {
		return nil, err
	}

	modelID, oneMillionContext := parseModelID(req.Model)

	input := &bedrockruntime.ConverseStreamInput{
		ModelId:         aws.String(modelID),
		Messages:        messages,
		ToolConfig:      toolConfig,
		InferenceConfig: buildInferenceConfig(req.MaxTokens, req.Temperature),
	}
	if req.System != "" {
		input.System = []types.SystemContentBlock{&types.SystemContentBlockMemberText{Value: req.System}}
	}
	if oneMillionContext {
		input.AdditionalModelRequestFields = document.NewLazyDocument(map[string]any{
			"anthropic_beta": []string{oneMillionContextBeta},
		})
	}

	resp, err := p.client.ConverseStream(ctx, input)
	if err != nil {
		return nil, wrapCredentialError(fmt.Errorf("bedrock ConverseStream: %w", err))
	}

	out := make(chan StreamEvent, 16)

	go func() {
		defer close(out)
		defer resp.GetStream().Close()

		// Content block index -> in-progress tool_use id/name, since
		// Bedrock's delta events key off index rather than tool id.
		type pending struct {
			id, name string
			args     strings.Builder
		}
		toolByIndex := map[int32]*pending{}

		send := func(ev StreamEvent) bool {
			select {
			case out <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}

		for streamEvent := range resp.GetStream().Events() {
			switch e := streamEvent.(type) {
			case *types.ConverseStreamOutputMemberContentBlockStart:
				if tu, ok := e.Value.Start.(*types.ContentBlockStartMemberToolUse); ok {
					idx := aws.ToInt32(e.Value.ContentBlockIndex)
					p := &pending{
						id:   aws.ToString(tu.Value.ToolUseId),
						name: aws.ToString(tu.Value.Name),
					}
					toolByIndex[idx] = p
					if !send(StreamEvent{Type: EventToolUseStart, ToolUseID: p.id, ToolName: p.name}) {
						return
					}
				}

			case *types.ConverseStreamOutputMemberContentBlockDelta:
				idx := aws.ToInt32(e.Value.ContentBlockIndex)
				switch d := e.Value.Delta.(type) {
				case *types.ContentBlockDeltaMemberText:
					if !send(StreamEvent{Type: EventTextDelta, TextDelta: d.Value}) {
						return
					}
				case *types.ContentBlockDeltaMemberToolUse:
					if p, ok := toolByIndex[idx]; ok {
						frag := aws.ToString(d.Value.Input)
						p.args.WriteString(frag)
						if !send(StreamEvent{Type: EventToolUseInputDelta, ToolUseID: p.id, InputDelta: frag}) {
							return
						}
					}
				}

			case *types.ConverseStreamOutputMemberContentBlockStop:
				idx := aws.ToInt32(e.Value.ContentBlockIndex)
				if p, ok := toolByIndex[idx]; ok {
					if !send(StreamEvent{Type: EventToolUseEnd, ToolUseID: p.id, ToolInput: json.RawMessage(p.args.String())}) {
						return
					}
				}

			case *types.ConverseStreamOutputMemberMessageStop:
				if !send(StreamEvent{Type: EventMessageStop, StopReason: mapBedrockStopReason(e.Value.StopReason)}) {
					return
				}

			case *types.ConverseStreamOutputMemberMetadata:
				if u := e.Value.Usage; u != nil {
					if !send(StreamEvent{Type: EventUsage, InputTokens: int(aws.ToInt32(u.InputTokens)), OutputTokens: int(aws.ToInt32(u.OutputTokens))}) {
						return
					}
				}
			}
		}

		if err := resp.GetStream().Err(); err != nil {
			send(StreamEvent{Type: EventError, Err: err})
		}
	}()

	return out, nil
}
