package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
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

	// ReasoningEffort is the OpenAI-compatible spelling of how hard to
	// think: "low", "medium", "high". Sent only when a profile asked for
	// one, which is what keeps this safe on a server that has never heard
	// of it — most ignore an unknown field, and the ones that refuse it
	// only ever see it from somebody who set it on purpose.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type oaStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type oaStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			// Reasoning, under both names it arrives with.
			//
			// Not in the OpenAI API and in most of what speaks its
			// protocol: DeepSeek introduced reasoning_content and vLLM,
			// SGLang, LM Studio, llama.cpp and Ollama followed it, while
			// OpenRouter and a few others send reasoning. A server sends
			// one or the other, never both, so reading both costs nothing
			// and is the difference between seeing a local model think
			// and watching it run tools in silence.
			//
			// It is displayed and then forgotten: broadcast to the
			// clients, never written to the session log, and never sent
			// back — toOpenAIMessages has no case for a thinking block,
			// which is what keeps a reply that reasoned from growing a
			// field on the way back in.
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
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
					writeTextBlock(&text, b.Text)
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
					writeTextBlock(&text, b.Text)
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

// writeTextBlock appends one text block to a message being flattened into
// the single string this wire format has for the several blocks a message
// may hold.
//
// Separated by a blank line, because two blocks are two things that were
// said and this used to join them with nothing at all. The shape that
// exposed it is the one every compaction leaves: the summary replaces the
// history as one user message, so the next prompt is a second user
// message, sendableHistory merges them to keep the alternation the other
// providers require, and the person's question arrived spliced onto the
// end of a sentence about something else — "...they discussed three
// files.fourth question, after the compaction". Anthropic and Bedrock
// were never affected, because both keep the blocks apart on the wire;
// this is the one adapter that has to choose, and it was choosing badly.
//
// A single block is untouched, which is every ordinary turn: nothing is
// written before the first non-empty block, and an empty one is not a
// paragraph anybody wrote.
func writeTextBlock(b *strings.Builder, text string) {
	if text == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString(text)
}

// openAIEffort is the value for "reasoning_effort", or "" for a request
// that should not carry the field at all.
func openAIEffort(e Effort) string {
	switch e {
	case EffortLow, EffortMedium, EffortHigh:
		return string(e)
	case EffortXHigh:
		// The field's vocabulary stops at high. The level still reaches
		// the models that have a word for it, muse among them, through
		// their system prompt: see museReasoningLine in internal/agent.
		return string(EffortHigh)
	}
	return ""
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
		// "off" is not sent as a level. The field's own vocabulary is
		// low/medium/high, servers disagree about whether there is a word
		// for "none", and asking for something no server agrees on is a
		// worse answer than saying nothing — which is what a model's own
		// default already is.
		ReasoningEffort: openAIEffort(req.Effort),
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
		// name is a builder, not a string, for the same reason args is:
		// the OpenAI streaming shape lets a function name arrive across
		// deltas for one index, and several local servers do exactly
		// that. Assigning the last fragment — or, as this did, keeping
		// the first — silently renames the tool, and the name is what
		// picks which tool runs. "read_file" split in two arrived as
		// "read_", which the registry then reported as a tool that does
		// not exist.
		type pending struct {
			id      string
			name    strings.Builder
			args    strings.Builder
			started bool
		}
		calls := map[int]*pending{}
		flushed := false
		// Whether reasoning is currently open, so it can be closed once.
		reasoned := false

		// endReasoning closes the reasoning block. No text travels with
		// it: the deltas were the whole of it, and the block a thinking
		// end produces is dropped by toOpenAIMessages rather than sent
		// back, which is what makes carrying the text here pointless and
		// carrying a signature impossible — this protocol has none.
		endReasoning := func() bool {
			select {
			case out <- StreamEvent{Type: EventThinkingEnd}:
				return true
			case <-ctx.Done():
				return false
			}
		}

		// callID is the id this tool call will be answered under.
		//
		// The real API always sends one, and several local servers do not:
		// they stream a tool call with a name, arguments, and no id at all.
		// An empty id travels all the way back as a tool_result nothing can
		// be matched to, so one is made up here — the only requirement is
		// that it is stable within the reply, which the stream index is.
		callID := func(index int, p *pending) string {
			if p.id == "" {
				p.id = fmt.Sprintf("call_%d", index)
			}
			return p.id
		}

		// startCall announces a tool call once, with its whole name.
		//
		// Deferred until the name can be known to be complete, which in
		// this format is when the arguments begin: a name and its
		// arguments are separate fields, and no server sends the second
		// before finishing the first. A call that never carries arguments
		// is announced at the flush instead, so every End still has a
		// Start to pair with.
		startCall := func(index int, p *pending) bool {
			if p.started {
				return true
			}
			p.started = true
			select {
			case out <- StreamEvent{Type: EventToolUseStart, ToolUseID: callID(index, p), ToolName: p.name.String()}:
				return true
			case <-ctx.Done():
				return false
			}
		}

		// flushCalls closes out every tool call the reply asked for, in the
		// index order the model issued them — tools are executed in the
		// order these arrive, and the model's own ordering is usually the
		// point (read a file, then edit what was read).
		//
		// Called both when a finish_reason arrives and, failing that, when
		// the stream simply ends. The second case is not hypothetical: a
		// local server that closes after [DONE] without ever sending a
		// finish_reason left every tool call it had just streamed sitting in
		// this map, so the turn ended with the model having asked to run
		// something and nothing having run. That is what "it stops
		// mid-task, and one more prompt carries on" looked like.
		flushCalls := func() bool {
			if flushed {
				return false
			}
			flushed = true
			indexes := make([]int, 0, len(calls))
			for i := range calls {
				indexes = append(indexes, i)
			}
			sort.Ints(indexes)
			for _, i := range indexes {
				p := calls[i]
				if !startCall(i, p) {
					return false
				}
				select {
				case out <- StreamEvent{Type: EventToolUseEnd, ToolUseID: callID(i, p), ToolInput: json.RawMessage(p.args.String())}:
				case <-ctx.Done():
					return false
				}
			}
			return len(calls) > 0
		}

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
			// Usage first, and outside the no-choices branch: the OpenAI
			// API sends it on a final chunk of its own, but vLLM and
			// several compatible proxies attach it to a chunk that still
			// carries a choice. Reading it only when there were no
			// choices dropped their token counts silently, which shows up
			// as a context meter that never moves.
			if chunk.Usage != nil {
				select {
				case out <- StreamEvent{Type: EventUsage, InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens}:
				case <-ctx.Done():
					return
				}
			}
			if len(chunk.Choices) == 0 {
				continue
			}
			choice := chunk.Choices[0]

			if reasoning := choice.Delta.ReasoningContent + choice.Delta.Reasoning; reasoning != "" {
				reasoned = true
				select {
				case out <- StreamEvent{Type: EventThinkingDelta, ThinkingDelta: reasoning}:
				case <-ctx.Done():
					return
				}
			}

			// The reasoning is over the moment anything else arrives.
			//
			// There is no end-of-reasoning marker in this protocol — the
			// field simply stops appearing — so the first content or tool
			// call is the signal. Closing it matters to the clients: the
			// TUI's status line says "thinking" until it is told
			// otherwise, and the Web UI leaves the block open.
			if reasoned && (choice.Delta.Content != "" || len(choice.Delta.ToolCalls) > 0) {
				reasoned = false
				if !endReasoning() {
					return
				}
			}

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
					p.name.WriteString(tc.Function.Name)
				}
				if tc.Function.Arguments != "" {
					if !startCall(tc.Index, p) {
						return
					}
					p.args.WriteString(tc.Function.Arguments)
					select {
					case out <- StreamEvent{Type: EventToolUseInputDelta, ToolUseID: callID(tc.Index, p), InputDelta: tc.Function.Arguments}:
					case <-ctx.Done():
						return
					}
				}
			}

			if choice.FinishReason != "" {
				// A reply that reasoned and then stopped without saying
				// anything closes here. Rare, and the cost of missing it
				// is a status line stuck on "thinking" for the rest of
				// the session.
				if reasoned {
					reasoned = false
					if !endReasoning() {
						return
					}
				}
				hadCalls := flushCalls()
				// A reply that asked for tools asked for tools, whatever
				// the server called the reason it stopped. "stop"
				// alongside tool_calls is common on local servers, and
				// taking it at its word ended the turn with the calls
				// never run.
				reason := mapFinishReason(choice.FinishReason)
				if hadCalls && reason == "end_turn" {
					reason = "tool_use"
				}
				select {
				case out <- StreamEvent{Type: EventMessageStop, StopReason: reason}:
				case <-ctx.Done():
					return
				}
			}
		}
		if err := scanner.Err(); err != nil {
			emitErr(fmt.Errorf("read stream: %w", err))
			return
		}
		// And the same for a stream that simply ended.
		if reasoned {
			reasoned = false
			if !endReasoning() {
				return
			}
		}
		// The stream ended without a finish_reason ever arriving. Whatever
		// the server meant by that, the tool calls it streamed are still
		// the ones the model asked for.
		if flushCalls() {
			select {
			case out <- StreamEvent{Type: EventMessageStop, StopReason: "tool_use"}:
			case <-ctx.Done():
			}
		}
	}()

	return out, nil
}
