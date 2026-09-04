package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

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
	mu      sync.Mutex
	client  bedrockClient
	region  string
	profile string
	load    bedrockClientLoader
}

type bedrockClient interface {
	ConverseStream(context.Context, *bedrockruntime.ConverseStreamInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseStreamOutput, error)
}

type bedrockClientLoader func(context.Context, string, string) (bedrockClient, error)

// NewBedrock records how to reach Bedrock without reading AWS configuration.
// The SDK config and credential chain are opened on the first Chat call. A
// configured but unused Bedrock provider must not prevent a local-only daemon
// from starting, especially when global and project config files are merged.
func NewBedrock(region, profile string) *Bedrock {
	return &Bedrock{region: region, profile: profile, load: loadBedrockClient}
}

// loadBedrockClient selects a named AWS profile, when configured, via the
// shared config and credential files instead of the default chain's usual
// resolution order.
func loadBedrockClient(ctx context.Context, region, profile string) (bedrockClient, error) {
	// The same client the HTTP providers use, so "/debug-log" sees these
	// calls too. The SDK signs the request in its own middleware, before
	// any transport runs, and the transport hands on the identical bytes,
	// so the signature is unaffected. What comes back is binary
	// event-stream frames, and they go into the log as they arrive: a
	// frame nobody can read is still evidence that it arrived.
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
		awsconfig.WithHTTPClient(debugClient()),
	}
	if profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return bedrockruntime.NewFromConfig(cfg), nil
}

// clientFor initializes the SDK client once it is actually needed. A failed
// load is deliberately not cached, so fixing an AWS profile while the daemon
// is running lets the next request retry without a restart.
func (p *Bedrock) clientFor(ctx context.Context) (bedrockClient, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		return p.client, nil
	}
	client, err := p.load(ctx, p.region, p.profile)
	if err != nil {
		return nil, err
	}
	p.client = client
	return client, nil
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
// reasoningRejected adds the sentence somebody needs when a Bedrock
// account refuses a request because of the reasoning parameter.
//
// It exists because this is the one part of the feature that cannot be
// verified from here: Converse takes the model's native parameters and
// which spelling a given account accepts is not in the SDK, the repo, or
// anything a test can reach. An unexplained ValidationException naming a
// field nobody set on purpose is the worst version of being wrong about
// it; a sentence that names the setting and how to turn it off is the
// least bad.
func reasoningRejected(err error, effort Effort) error {
	if err == nil || effort == EffortUnset || effort == EffortOff {
		return err
	}
	lower := strings.ToLower(err.Error())
	if !strings.Contains(lower, "validation") && !strings.Contains(lower, "malformed") {
		return err
	}
	if !strings.Contains(lower, bedrockThinkingField) && !strings.Contains(lower, "reasoning") &&
		!strings.Contains(lower, "additionalmodelrequestfields") {
		return err
	}
	return fmt.Errorf("%w\n\nhint: this request asked for %q-effort reasoning, which Bedrock carries as "+
		"the %q parameter, and this model or account did not accept it. Turn it off for this conversation "+
		"with \"/effort off\", or remove \"effort\" from the profile", err, effort, bedrockThinkingField)
}

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

// toBedrockMessages converts the history, keeping reasoning blocks only
// where the API needs them.
//
// The SDK says it in its own doc comment on ReasoningTextBlock: "If you
// pass a reasoning block back to the API in a multi-turn conversation,
// include the text and its signature unmodified." A tool-use turn still
// in flight must therefore return the reasoning that preceded the tool
// call. Every earlier turn must not — that reasoning is spent, and
// re-sending pages of it every request costs tokens for nothing.
//
// So exactly one message carries them: the last assistant message, which
// is the only one a continuation can be continuing. The same rule the
// Anthropic adapter follows, for the same reason.
func toBedrockMessages(msgs []Message) ([]types.Message, error) {
	lastAssistant := -1
	for i, m := range msgs {
		if m.Role == RoleAssistant {
			lastAssistant = i
		}
	}

	out := make([]types.Message, 0, len(msgs))
	for i, m := range msgs {
		role := types.ConversationRoleUser
		if m.Role == RoleAssistant {
			role = types.ConversationRoleAssistant
		}

		blocks := make([]types.ContentBlock, 0, len(m.Content))
		for _, b := range m.Content {
			switch b.Type {
			case BlockThinking:
				// Unsigned means this process assembled the block rather
				// than receiving it — a rehydrated history, a test. Sending
				// it would claim an attestation that does not exist, and
				// the API refuses the request rather than the block.
				if i != lastAssistant || b.Signature == "" {
					continue
				}
				blocks = append(blocks, &types.ContentBlockMemberReasoningContent{
					Value: &types.ReasoningContentBlockMemberReasoningText{
						Value: types.ReasoningTextBlock{
							Text:      aws.String(b.Text),
							Signature: aws.String(b.Signature),
						},
					},
				})

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

// bedrockExtraFields builds additionalModelRequestFields: the model's own
// native parameters, which Converse has no first-class field for.
//
// One map rather than two assignments, which is the bug this shape
// forecloses. The field is a single document: whichever of the two
// features wrote it last used to win, so asking for reasoning on a
// million-token model would have silently dropped the beta header that
// makes it a million-token model.
//
// Reasoning goes through here because ConverseStreamInput has no field
// for it — grep the SDK for "reasoning" outside the content types and
// there is nothing. Its shape is the model's, and for Claude that is the
// same object the Anthropic API takes, which is why anthropicThinking
// answers for both adapters rather than each having its own table.
func bedrockExtraFields(oneMillionContext bool, model string, effort Effort, maxTokens int) map[string]any {
	fields := map[string]any{}
	if oneMillionContext {
		fields["anthropic_beta"] = []string{oneMillionContextBeta}
	}
	if th := anthropicThinking(model, effort, maxTokens); th != nil {
		if doc := asDocumentValue(th); doc != nil {
			fields[bedrockThinkingField] = doc
		}
	}
	return fields
}

// asDocumentValue converts a value to the plain map a smithy document
// serializes correctly.
//
// It exists because of a difference worth stating rather than working
// around silently: document.NewLazyDocument serializes a Go struct by its
// FIELD names, not its json tags, so handing it anthThinking directly put
// {"Type":"enabled","BudgetTokens":16384} on the wire — a shape no model
// has ever accepted. Round-tripping through encoding/json applies the
// tags, which keeps those tags the single definition of what extended
// thinking looks like for both adapters instead of this file keeping a
// second copy of the key names.
//
// A value that cannot make the trip comes back nil, and a nil entry is
// dropped by the caller rather than sent as null.
func asDocumentValue(v any) map[string]any {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// reasoningDelta splits one reasoning delta into its two halves.
//
// Two halves rather than one because they arrive separately and in that
// order: the text streams, and the signature that attests to it comes
// after the text is complete. Neither is usable alone — the text without
// the signature cannot be sent back, and the signature without the text
// signs nothing.
//
// Redacted reasoning returns neither, deliberately. It is bytes the
// provider encrypted for its own reasons: it cannot be shown, and it is
// not the block a continuation has to return.
func reasoningDelta(d types.ReasoningContentBlockDelta) (text, signature string) {
	switch r := d.(type) {
	case *types.ReasoningContentBlockDeltaMemberText:
		return r.Value, ""
	case *types.ReasoningContentBlockDeltaMemberSignature:
		return "", r.Value
	}
	return "", ""
}

func toBedrockTools(tools []Tool, cachePrefix bool) (*types.ToolConfiguration, error) {
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
	if cachePrefix {
		// A cache point is its own entry in the list rather than a field
		// on the last tool, which is how Converse spells it: everything
		// before the marker is the cacheable prefix.
		specs = append(specs, &types.ToolMemberCachePoint{Value: types.CachePointBlock{Type: types.CachePointTypeDefault}})
	}
	return &types.ToolConfiguration{Tools: specs}, nil
}

// oneMillionContextBeta is the Anthropic beta flag that unlocks the
// 1M-token context window on supported Claude Sonnet models. On direct
// Anthropic API calls this is sent as an "anthropic-beta" HTTP header;
// Bedrock's Converse API has no such header, so it's passed via
// AdditionalModelRequestFields instead (see parseModelID/Chat below).
const oneMillionContextBeta = "context-1m-2025-08-07"

// bedrockThinkingField is the key extended thinking travels under inside
// additionalModelRequestFields.
//
// Anthropic's own parameter name, and the choice is evidence-based rather
// than obvious. Converse has no first-class field for reasoning, so what
// goes in this document is the model's native parameters — and the
// precedent is directly above: "anthropic_beta" is an Anthropic-native
// name, it goes through this same document on this same API, and it
// works. The value is the same object the direct Anthropic adapter
// builds, which is why one function answers for both rather than each
// keeping its own table of budgets.
//
// A constant because it is the one thing here that could be wrong. If a
// Bedrock account rejects the request naming this field, this line is the
// fix — and reasoningRejected below makes that rejection say so instead
// of arriving as an unexplained validation error.
const bedrockThinkingField = "thinking"

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
	if req.CachePrefix {
		// The conversation's own cache points, mirroring the Anthropic
		// client: the end of each of the last two messages. Bedrock
		// spells a breakpoint as a content block of its own rather than
		// a field on one, and ignores cache points on models without
		// prompt caching, which is the same "request, not guarantee"
		// contract CachePrefix already documents.
		marked := 0
		for i := len(messages) - 1; i >= 0 && marked < 2; i-- {
			if len(messages[i].Content) == 0 {
				continue
			}
			messages[i].Content = append(messages[i].Content, &types.ContentBlockMemberCachePoint{
				Value: types.CachePointBlock{Type: types.CachePointTypeDefault},
			})
			marked++
		}
	}
	toolConfig, err := toBedrockTools(req.Tools, req.CachePrefix)
	if err != nil {
		return nil, err
	}

	modelID, oneMillionContext := parseModelID(req.Model)

	input := &bedrockruntime.ConverseStreamInput{
		ModelId:    aws.String(modelID),
		Messages:   messages,
		ToolConfig: toolConfig,
		// Temperature is dropped when reasoning is asked for: the API
		// fixes it while a model is thinking and refuses a request that
		// also sets one. See the same rule in the Anthropic adapter.
		InferenceConfig: buildInferenceConfig(req.MaxTokens, temperatureFor(req)),
	}
	// One SystemContentBlock per prompt asset when the blocks arrived
	// with their seams; the folded string only when they did not. The
	// cache point goes after the last block either way and caches
	// everything before it.
	switch {
	case len(req.SystemBlocks) > 0:
		for _, b := range req.SystemBlocks {
			input.System = append(input.System, &types.SystemContentBlockMemberText{Value: b.Text})
		}
	case req.System != "":
		input.System = []types.SystemContentBlock{&types.SystemContentBlockMemberText{Value: req.System}}
	}
	if len(input.System) > 0 && req.CachePrefix {
		input.System = append(input.System, &types.SystemContentBlockMemberCachePoint{
			Value: types.CachePointBlock{Type: types.CachePointTypeDefault},
		})
	}
	if extra := bedrockExtraFields(oneMillionContext, req.Model, req.Effort, req.MaxTokens); len(extra) > 0 {
		input.AdditionalModelRequestFields = document.NewLazyDocument(extra)
	}

	client, err := p.clientFor(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := client.ConverseStream(ctx, input)
	if err != nil {
		return nil, reasoningRejected(wrapCredentialError(fmt.Errorf("bedrock ConverseStream: %w", err)), req.Effort)
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

		// Reasoning accumulates per content-block index, the same way,
		// because a stream can carry several blocks at once and the
		// signature arrives separately from the text it signs.
		type pendingThinking struct {
			text      strings.Builder
			signature strings.Builder
		}
		thinkingByIndex := map[int32]*pendingThinking{}

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
				case *types.ContentBlockDeltaMemberReasoningContent:
					text, signature := reasoningDelta(d.Value)
					p := thinkingByIndex[idx]
					if p == nil {
						p = &pendingThinking{}
						thinkingByIndex[idx] = p
					}
					p.text.WriteString(text)
					p.signature.WriteString(signature)
					if text != "" && !send(StreamEvent{Type: EventThinkingDelta, ThinkingDelta: text}) {
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
				if p, ok := thinkingByIndex[idx]; ok {
					if !send(StreamEvent{
						Type: EventThinkingEnd, ThinkingDelta: p.text.String(), Signature: p.signature.String(),
					}) {
						return
					}
					delete(thinkingByIndex, idx)
				}
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
					if !send(StreamEvent{
						Type:             EventUsage,
						InputTokens:      int(aws.ToInt32(u.InputTokens)),
						OutputTokens:     int(aws.ToInt32(u.OutputTokens)),
						CacheReadTokens:  int(aws.ToInt32(u.CacheReadInputTokens)),
						CacheWriteTokens: int(aws.ToInt32(u.CacheWriteInputTokens)),
					}) {
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
