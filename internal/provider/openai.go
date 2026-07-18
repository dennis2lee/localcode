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

// OpenAICompat talks to any OpenAI-compatible /v1/chat/completions endpoint
// (LM Studio, vLLM, etc.) and translates to/from the internal block model.
type OpenAICompat struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
}

func NewOpenAICompat(baseURL, apiKey string) *OpenAICompat {
	return &OpenAICompat{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Client:  http.DefaultClient,
	}
}

// --- wire types (OpenAI chat/completions) ---

type oaMessage struct {
	Role       string       `json:"role"`
	Content    string       `json:"content,omitempty"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type oaRequest struct {
	Model       string      `json:"model"`
	Messages    []oaMessage `json:"messages"`
	Tools       []oaTool    `json:"tools,omitempty"`
	Stream      bool        `json:"stream"`
	MaxTokens   int         `json:"max_tokens,omitempty"`
	Temperature float64     `json:"temperature,omitempty"`

	// StreamOptions requests a final usage-only chunk (empty "choices")
	// at the end of the stream — an OpenAI-compat server that doesn't
	// recognize this field just ignores it, so it's safe to always send.
	StreamOptions *oaStreamOptions `json:"stream_options,omitempty"`
}

type oaStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type oaStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`

	// Usage arrives on its own final chunk with empty Choices, only when
	// the request set stream_options.include_usage.
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage,omitempty"`
}

// toOpenAIMessages translates our block-based messages (plus system prompt)
// into the OpenAI role/content/tool_calls shape. Anthropic-style tool_result
// blocks (carried inside a "user" message) become separate role:"tool"
// messages, since OpenAI has no equivalent block-in-message concept.
func toOpenAIMessages(system string, msgs []Message) []oaMessage {
	var out []oaMessage
	if system != "" {
		out = append(out, oaMessage{Role: "system", Content: system})
	}

	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			var text strings.Builder
			for _, b := range m.Content {
				switch b.Type {
				case BlockText:
					text.WriteString(b.Text)
				case BlockToolResult:
					out = append(out, oaMessage{
						Role:       "tool",
						Content:    b.ToolResultContent,
						ToolCallID: b.ToolUseID,
					})
				}
			}
			if text.Len() > 0 {
				out = append(out, oaMessage{Role: "user", Content: text.String()})
			}

		case RoleAssistant:
			var text strings.Builder
			var calls []oaToolCall
			for _, b := range m.Content {
				switch b.Type {
				case BlockText:
					text.WriteString(b.Text)
				case BlockToolUse:
					tc := oaToolCall{ID: b.ToolUseID, Type: "function"}
					tc.Function.Name = b.ToolName
					tc.Function.Arguments = string(b.ToolInput)
					calls = append(calls, tc)
				}
			}
			out = append(out, oaMessage{Role: "assistant", Content: text.String(), ToolCalls: calls})
		}
	}
	return out
}

func toOpenAITools(tools []Tool) []oaTool {
	out := make([]oaTool, 0, len(tools))
	for _, t := range tools {
		var ot oaTool
		ot.Type = "function"
		ot.Function.Name = t.Name
		ot.Function.Description = t.Description
		ot.Function.Parameters = t.InputSchema
		out = append(out, ot)
	}
	return out
}

func mapFinishReason(r string) string {
	switch r {
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	default:
		return "end_turn"
	}
}

func (p *OpenAICompat) Chat(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	body := oaRequest{
		Model:         req.Model,
		Messages:      toOpenAIMessages(req.System, req.Messages),
		Tools:         toOpenAITools(req.Tools),
		Stream:        true,
		MaxTokens:     req.MaxTokens,
		Temperature:   req.Temperature,
		StreamOptions: &oaStreamOptions{IncludeUsage: true},
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	}

	resp, err := p.Client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return nil, fmt.Errorf("openai-compat endpoint returned %d: %s", resp.StatusCode, buf.String())
	}

	out := make(chan StreamEvent, 16)

	go func() {
		defer resp.Body.Close()
		defer close(out)

		// Track partial tool_call argument accumulation per stream index,
		// since providers send tool_calls incrementally across chunks.
		type pending struct {
			id, name string
			args     strings.Builder
			started  bool
		}
		calls := map[int]*pending{}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		emitErr := func(err error) {
			select {
			case out <- StreamEvent{Type: EventError, Err: err}:
			case <-ctx.Done():
			}
		}

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}

			var chunk oaStreamChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue // ignore malformed keep-alive/comment lines
			}
			if len(chunk.Choices) == 0 {
				if chunk.Usage != nil {
					select {
					case out <- StreamEvent{Type: EventUsage, InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens}:
					case <-ctx.Done():
						return
					}
				}
				continue
			}
			choice := chunk.Choices[0]

			if choice.Delta.Content != "" {
				select {
				case out <- StreamEvent{Type: EventTextDelta, TextDelta: choice.Delta.Content}:
				case <-ctx.Done():
					return
				}
			}

			for _, tc := range choice.Delta.ToolCalls {
				p, ok := calls[tc.Index]
				if !ok {
					p = &pending{}
					calls[tc.Index] = p
				}
				if tc.ID != "" {
					p.id = tc.ID
				}
				if tc.Function.Name != "" {
					p.name = tc.Function.Name
				}
				if !p.started && p.id != "" && p.name != "" {
					p.started = true
					select {
					case out <- StreamEvent{Type: EventToolUseStart, ToolUseID: p.id, ToolName: p.name}:
					case <-ctx.Done():
						return
					}
				}
				if tc.Function.Arguments != "" {
					p.args.WriteString(tc.Function.Arguments)
					select {
					case out <- StreamEvent{Type: EventToolUseInputDelta, ToolUseID: p.id, InputDelta: tc.Function.Arguments}:
					case <-ctx.Done():
						return
					}
				}
			}

			if choice.FinishReason != "" {
				for _, p := range calls {
					select {
					case out <- StreamEvent{Type: EventToolUseEnd, ToolUseID: p.id, ToolInput: json.RawMessage(p.args.String())}:
					case <-ctx.Done():
						return
					}
				}
				select {
				case out <- StreamEvent{Type: EventMessageStop, StopReason: mapFinishReason(choice.FinishReason)}:
				case <-ctx.Done():
					return
				}
			}
		}
		if err := scanner.Err(); err != nil {
			emitErr(fmt.Errorf("read stream: %w", err))
		}
	}()

	return out, nil
}
