package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// anthropicAPIVersion is the API version header Anthropic's Messages API
// requires; bumping it is a deliberate, versioned change on their side.
const anthropicAPIVersion = "2023-06-01"

const anthropicDefaultBaseURL = "https://api.anthropic.com"

// AnthropicDirect talks to Anthropic's own Messages API
// (api.anthropic.com/v1/messages) using a personal API key from
// console.anthropic.com — usage-billed separately from a claude.ai Pro/Max
// subscription, not a substitute for it. Since our internal Message/Block
// model is already Anthropic-shaped (see package doc), this translation is
// close to a 1:1 passthrough, unlike the OpenAI-compat adapter.
type AnthropicDirect struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
}

func NewAnthropicDirect(apiKey string) *AnthropicDirect {
	return &AnthropicDirect{
		BaseURL: anthropicDefaultBaseURL,
		APIKey:  apiKey,
		Client:  http.DefaultClient,
	}
}

// --- wire types (Anthropic Messages API) ---

type anthMessage struct {
	Role    string             `json:"role"`
	Content []anthContentBlock `json:"content"`
}

type anthContentBlock struct {
	Type string `json:"type"`

	Text string `json:"text,omitempty"` // text

	// CacheControl marks this block as the end of a cacheable prefix.
	// Everything before it, this block included, is stored and served
	// from cache on a later request whose prefix matches byte for byte.
	CacheControl *anthCacheControl `json:"cache_control,omitempty"`

	ID    string          `json:"id,omitempty"`    // tool_use
	Name  string          `json:"name,omitempty"`  // tool_use
	Input json.RawMessage `json:"input,omitempty"` // tool_use

	ToolUseID string `json:"tool_use_id,omitempty"` // tool_result
	Content   string `json:"content,omitempty"`     // tool_result
	IsError   bool   `json:"is_error,omitempty"`    // tool_result
}

type anthTool struct {
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	InputSchema  json.RawMessage   `json:"input_schema"`
	CacheControl *anthCacheControl `json:"cache_control,omitempty"`
}

// anthCacheControl is the only kind there is. "ephemeral" means the
// five-minute TTL, which is the right one here: an agent turn is followed
// by another agent turn within seconds, and the one-hour TTL costs twice
// as much to write for a prefix that will be rewritten anyway.
type anthCacheControl struct {
	Type string `json:"type"`
}

func ephemeral() *anthCacheControl { return &anthCacheControl{Type: "ephemeral"} }

type anthRequest struct {
	Model string `json:"model"`
	// A string ordinarily, and an array of one text block when a cache
	// breakpoint has to be attached to it — the API accepts both, and the
	// plain string is kept for the uncached case so the request on the
	// wire is exactly what it has always been.
	System      any           `json:"system,omitempty"`
	Messages    []anthMessage `json:"messages"`
	Tools       []anthTool    `json:"tools,omitempty"`
	MaxTokens   int           `json:"max_tokens"`
	Temperature float64       `json:"temperature,omitempty"`
	Stream      bool          `json:"stream"`
}

// anthStreamEvent covers every field used across the handful of SSE event
// types this client cares about (content_block_start/delta/stop,
// message_delta, message_stop, error); unused fields for a given "type"
// are simply left zero.
type anthStreamEvent struct {
	Type string `json:"type"`

	Index int `json:"index"`

	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block,omitempty"`

	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta,omitempty"`

	// Message carries message_start's nested usage (input tokens are
	// known up front; output_tokens there is usually 0/an early estimate).
	Message *struct {
		Usage *anthUsage `json:"usage"`
	} `json:"message,omitempty"`

	// Usage is message_delta's top-level usage field (cumulative
	// output_tokens for the response so far — input_tokens isn't
	// repeated here since it doesn't change mid-response).
	Usage *anthUsage `json:"usage,omitempty"`

	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type anthUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	// What the prompt cache did for this request. Reported separately
	// from input_tokens by the API, and kept separate here: they are
	// priced differently, and "1200 read from cache" is the number that
	// says whether the breakpoint is working.
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

func toAnthropicMessages(msgs []Message) []anthMessage {
	out := make([]anthMessage, 0, len(msgs))
	for _, m := range msgs {
		blocks := make([]anthContentBlock, 0, len(m.Content))
		for _, b := range m.Content {
			switch b.Type {
			case BlockText:
				blocks = append(blocks, anthContentBlock{Type: "text", Text: b.Text})
			case BlockToolUse:
				input := b.ToolInput
				if len(input) == 0 {
					input = json.RawMessage("{}")
				}
				blocks = append(blocks, anthContentBlock{Type: "tool_use", ID: b.ToolUseID, Name: b.ToolName, Input: input})
			case BlockToolResult:
				blocks = append(blocks, anthContentBlock{Type: "tool_result", ToolUseID: b.ToolUseID, Content: b.ToolResultContent, IsError: b.IsError})
			}
		}
		out = append(out, anthMessage{Role: string(m.Role), Content: blocks})
	}
	return out
}

// markConversationCache sets a cache breakpoint on the last block of
// each of the last two messages. See the CachePrefix comment in Chat.
func markConversationCache(msgs []anthMessage) {
	marked := 0
	for i := len(msgs) - 1; i >= 0 && marked < 2; i-- {
		if n := len(msgs[i].Content); n > 0 {
			msgs[i].Content[n-1].CacheControl = ephemeral()
			marked++
		}
	}
}

func toAnthropicTools(tools []Tool) []anthTool {
	out := make([]anthTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, anthTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out
}

func mapAnthropicStopReason(r string) string {
	switch r {
	case "tool_use":
		return "tool_use"
	case "max_tokens":
		return "max_tokens"
	default:
		return "end_turn"
	}
}

func (p *AnthropicDirect) Chat(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	tools := toAnthropicTools(req.Tools)
	var system any = req.System
	if req.CachePrefix {
		// Two breakpoints, at the two places the request stops being the
		// same as last time: after the tool schemas and after the system
		// prompt. Both are byte-identical from turn to turn in an agent
		// session, and together they are the bulk of every request's fixed
		// cost.
		//
		if n := len(tools); n > 0 {
			tools[n-1].CacheControl = ephemeral()
		}
		if req.System != "" {
			system = []anthContentBlock{{Type: "text", Text: req.System, CacheControl: ephemeral()}}
		}
	}
	messages := toAnthropicMessages(req.Messages)
	if req.CachePrefix {
		// The conversation's own breakpoints: the last block of the last
		// two messages. In an agent session the history is append-only,
		// so the previous request's marked prefix is a prefix of this
		// one, the lookup reads it at the cache rate, and only the new
		// suffix is written at the premium — incremental, not a rewrite.
		// Two marks rather than one because a lookup only checks a
		// bounded distance behind each breakpoint, and one long tool
		// round can outrun it; the older mark is the fallback that keeps
		// the miss from reaching back to the start of the conversation.
		// With the two on tools and system above, that is four, which is
		// the API's limit.
		markConversationCache(messages)
	}
	body := anthRequest{
		Model:       req.Model,
		System:      system,
		Messages:    messages,
		Tools:       tools,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      true,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.BaseURL, "/")+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.APIKey)
	httpReq.Header.Set("anthropic-version", anthropicAPIVersion)

	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return nil, fmt.Errorf("anthropic API returned %d: %s", resp.StatusCode, buf.String())
	}

	out := make(chan StreamEvent, 16)

	go func() {
		defer resp.Body.Close()
		defer close(out)

		// Tracks which content-block index is a tool_use, so
		// content_block_delta events with an input_json_delta know which
		// tool_use_id/name to attach to their partial JSON, and accumulates
		// the full input so content_block_stop can report it in one piece
		// (matching the Bedrock/OpenAI-compat providers' own behavior).
		type pending struct {
			id, name string
			args     strings.Builder
		}
		toolByIndex := map[int]*pending{}

		// inputTokens is captured once from message_start (Anthropic
		// doesn't repeat it in message_delta's usage, which only reports
		// cumulative output_tokens).
		var inputTokens, cacheRead, cacheWrite int

		send := func(ev StreamEvent) bool {
			select {
			case out <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")

			var ev anthStreamEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				continue // ignore malformed/keep-alive lines
			}

			switch ev.Type {
			case "message_start":
				if ev.Message != nil && ev.Message.Usage != nil {
					inputTokens = ev.Message.Usage.InputTokens
					cacheRead = ev.Message.Usage.CacheReadInputTokens
					cacheWrite = ev.Message.Usage.CacheCreationInputTokens
					if !send(StreamEvent{
						Type: EventUsage, InputTokens: inputTokens, OutputTokens: ev.Message.Usage.OutputTokens,
						CacheReadTokens: cacheRead, CacheWriteTokens: cacheWrite,
					}) {
						return
					}
				}

			case "content_block_start":
				if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
					toolByIndex[ev.Index] = &pending{id: ev.ContentBlock.ID, name: ev.ContentBlock.Name}
					if !send(StreamEvent{Type: EventToolUseStart, ToolUseID: ev.ContentBlock.ID, ToolName: ev.ContentBlock.Name}) {
						return
					}
				}

			case "content_block_delta":
				if ev.Delta == nil {
					continue
				}
				switch ev.Delta.Type {
				case "text_delta":
					if !send(StreamEvent{Type: EventTextDelta, TextDelta: ev.Delta.Text}) {
						return
					}
				case "input_json_delta":
					if p, ok := toolByIndex[ev.Index]; ok {
						p.args.WriteString(ev.Delta.PartialJSON)
						if !send(StreamEvent{Type: EventToolUseInputDelta, ToolUseID: p.id, InputDelta: ev.Delta.PartialJSON}) {
							return
						}
					}
				}

			case "content_block_stop":
				if p, ok := toolByIndex[ev.Index]; ok {
					input := json.RawMessage(p.args.String())
					if len(input) == 0 {
						input = json.RawMessage("{}")
					}
					if !send(StreamEvent{Type: EventToolUseEnd, ToolUseID: p.id, ToolInput: input}) {
						return
					}
					delete(toolByIndex, ev.Index)
				}

			case "message_delta":
				if ev.Usage != nil {
					// message_delta repeats neither the input count nor
					// the cache counts, so the ones from message_start are
					// carried forward rather than reported as zero.
					if !send(StreamEvent{
						Type: EventUsage, InputTokens: inputTokens, OutputTokens: ev.Usage.OutputTokens,
						CacheReadTokens: cacheRead, CacheWriteTokens: cacheWrite,
					}) {
						return
					}
				}
				if ev.Delta != nil && ev.Delta.StopReason != "" {
					if !send(StreamEvent{Type: EventMessageStop, StopReason: mapAnthropicStopReason(ev.Delta.StopReason)}) {
						return
					}
				}

			case "error":
				msg := "anthropic stream error"
				if ev.Error != nil {
					msg = fmt.Sprintf("anthropic stream error (%s): %s", ev.Error.Type, ev.Error.Message)
				}
				send(StreamEvent{Type: EventError, Err: fmt.Errorf("%s", msg)})
				return
			}
		}
		if err := scanner.Err(); err != nil {
			send(StreamEvent{Type: EventError, Err: fmt.Errorf("read stream: %w", err)})
		}
	}()

	return out, nil
}
