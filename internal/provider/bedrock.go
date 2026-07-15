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

func NewBedrock(ctx context.Context, region string) (*Bedrock, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return &Bedrock{client: bedrockruntime.NewFromConfig(cfg)}, nil
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

	input := &bedrockruntime.ConverseStreamInput{
		ModelId:    aws.String(req.Model),
		Messages:   messages,
		ToolConfig: toolConfig,
		InferenceConfig: &types.InferenceConfiguration{
			MaxTokens:   aws.Int32(int32(req.MaxTokens)),
			Temperature: aws.Float32(float32(req.Temperature)),
		},
	}
	if req.System != "" {
		input.System = []types.SystemContentBlock{&types.SystemContentBlockMemberText{Value: req.System}}
	}

	resp, err := p.client.ConverseStream(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("bedrock ConverseStream: %w", err)
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
			}
		}

		if err := resp.GetStream().Err(); err != nil {
			send(StreamEvent{Type: EventError, Err: err})
		}
	}()

	return out, nil
}
