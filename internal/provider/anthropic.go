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
		Client:  debugClient(),
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

	// thinking. The signature is the API's attestation of the block, and
	// a continuation that sends the text without it is refused.
	//
	// omitempty here is right for every other block type, which never set
	// these — and wrong for a thinking block, which has to send both
	// whatever they hold. MarshalJSON below is what settles that; these
	// two tags only ever apply to the blocks that are not thinking.
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
}

// anthThinkingBlock is the wire shape of a thinking block.
//
// Its own type because its two fields are required and may be empty,
// which is the opposite of what every other block type needs from the
// same struct. On the current Claude models the reasoning text routinely
// *is* empty: `display` defaults to omitting it on Fable 5, Opus 5, Opus
// 4.8, Opus 4.7 and Sonnet 5, so the thinking happens and is billed and
// only the words are withheld.
//
// A thinking block has to go back unchanged on the next turn, and
// omitempty dropped that empty text on the way out — so the continuation
// carried {"type":"thinking","signature":"..."} and the API refused the
// whole request with "messages.N.content.0.thinking.thinking: Field
// required". The first turns of a conversation worked, because they had
// no thinking block to replay yet.
type anthThinkingBlock struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
	// A thinking block is the last block of its message when it is the
	// only one — the model thought and then said nothing — which is a
	// place markConversationCache lands a breakpoint.
	CacheControl *anthCacheControl `json:"cache_control,omitempty"`
}

// MarshalJSON sends a thinking block through its own shape and everything
// else through the shared one.
//
// Dispatching here rather than at the call site because the blocks travel
// as one []anthContentBlock, which cannot hold two types — and because
// the rule is about what the wire requires, which belongs next to the
// wire types rather than in the conversion that fills them.
func (b anthContentBlock) MarshalJSON() ([]byte, error) {
	if b.Type == "thinking" {
		return json.Marshal(anthThinkingBlock{
			Type:         b.Type,
			Thinking:     b.Thinking,
			Signature:    b.Signature,
			CacheControl: b.CacheControl,
		})
	}
	// A defined type with no methods of its own, or this would call
	// itself.
	type plain anthContentBlock
	return json.Marshal(plain(b))
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
	System   any           `json:"system,omitempty"`
	Messages []anthMessage `json:"messages"`
	Tools    []anthTool    `json:"tools,omitempty"`
	// ToolChoice is {"type":"none"} when the tools on the request may
	// not be called, and absent otherwise. Only alongside tools.
	ToolChoice  *anthToolChoice `json:"tool_choice,omitempty"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature float64         `json:"temperature,omitempty"`
	Stream      bool            `json:"stream"`
	Thinking    *anthThinking   `json:"thinking,omitempty"`
}

type anthToolChoice struct {
	Type string `json:"type"`
}

// anthThinking is extended thinking, in the two shapes the API has had.
//
// "adaptive" is the current one and takes no number: the model decides
// how much reasoning the request is worth. "enabled" is the older one and
// requires a budget in tokens. Which of the two a model accepts is a
// property of the model — the newest families reject a budget outright —
// so the choice is made from the id. See anthropicThinking.
type anthThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
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
		// Extended thinking arrives as its own two delta kinds: the
		// reasoning itself, then the signature that attests to it.
		Thinking  string `json:"thinking"`
		Signature string `json:"signature"`
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

// toAnthropicMessages converts the history, keeping thinking blocks only
// where the API needs them.
//
// The rule is narrow because both directions are wrong. A tool-use turn
// that is still in flight must send back the thinking the model produced
// before it asked for the tool, signature and all, or the continuation is
// refused. Every earlier turn must not: that reasoning has already been
// used, the API does not want it back, and re-sending pages of it every
// request costs tokens for nothing.
//
// "Where the API needs them" is therefore exactly one message — the last
// assistant message in the history, which is the only one a continuation
// can be continuing.
func toAnthropicMessages(msgs []Message) []anthMessage {
	lastAssistant := -1
	for i, m := range msgs {
		if m.Role == RoleAssistant {
			lastAssistant = i
		}
	}

	out := make([]anthMessage, 0, len(msgs))
	for i, m := range msgs {
		blocks := make([]anthContentBlock, 0, len(m.Content))
		for _, b := range m.Content {
			switch b.Type {
			case BlockThinking:
				if i != lastAssistant || b.Signature == "" {
					// An unsigned block is one this process assembled
					// rather than received — a rehydrated history, a test.
					// Sending it would be claiming an attestation that
					// does not exist.
					continue
				}
				blocks = append(blocks, anthContentBlock{Type: "thinking", Thinking: b.Text, Signature: b.Signature})
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
// adaptiveThinkingFamilies are the model families whose extended
// thinking takes no budget, and which refuse one outright.
//
// A list of families rather than a version comparison, because a model id
// is a vendor string and "is this newer than 4.6" is not a question a
// substring can answer. Matched on the id, lowercased, the way the quirk
// table matches.
var adaptiveThinkingFamilies = []string{
	"claude-opus-5", "claude-sonnet-5", "claude-fable-5", "claude-mythos-5",
	"claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6", "claude-sonnet-4-6",
}

// AnthropicAdaptiveThinking reports whether a model decides the size of
// its own reasoning rather than taking a budget. Exported because the
// difference is worth telling the person who set the level: on these
// families every level reaches the same switch.
func AnthropicAdaptiveThinking(model string) bool {
	id := strings.ToLower(model)
	for _, family := range adaptiveThinkingFamilies {
		if strings.Contains(id, family) {
			return true
		}
	}
	return false
}

// thinkingBudgets is what a level means on a model that takes a number.
var thinkingBudgets = map[Effort]int{
	EffortLow:    2048,
	EffortMedium: 8192,
	EffortHigh:   16384,
	// No larger tier exists on this wire. xhigh is a muse word; here it
	// means the most this API takes, which is the same as high.
	EffortXHigh: 16384,
}

// temperatureFor is the temperature a request may carry: its own, unless
// it is also asking the model to reason, in which case none.
//
// Temperature and extended thinking do not go together: the API fixes the
// temperature while a model is reasoning and refuses a request that also
// sets one. Dropping it is the answer that leaves both features usable —
// the alternative is that a profile with a temperature on it cannot ask
// for reasoning at all — and it is the same rule on both adapters, which
// is why it lives in one function rather than two.
func temperatureFor(req ChatRequest) float64 {
	if anthropicThinking(req.Model, req.Effort, req.MaxTokens) != nil {
		return 0
	}
	return req.Temperature
}

// answerReserve is how much of the output cap is kept back for the answer
// when reasoning takes a budget out of it.
//
// The budget is spent from max_tokens, so a budget equal to it leaves the
// model no room to say anything, and a budget larger than it is refused
// outright. Neither is a subtle failure: one is an empty reply and the
// other is a 400 on a profile that was working yesterday.
const answerReserve = 1024

// minThinkingBudget is the smallest budget worth asking for. Below this
// the request is a reasoning model with no room to reason, so nothing is
// asked for at all and the model answers as it would have.
const minThinkingBudget = 1024

// fitBudget shrinks a level's budget to what this request's output cap
// can actually pay for, and reports whether anything is left.
//
// The case is ordinary rather than exotic: the shipped example config
// pairs max_tokens 8192 with these profiles, and "high" is 16384. Without
// this, turning effort up on that profile is a 400 rather than more
// thinking.
func fitBudget(budget, maxTokens int) (int, bool) {
	if maxTokens <= 0 {
		// Nothing said about the cap, so nothing to fit against.
		return budget, true
	}
	if room := maxTokens - answerReserve; budget > room {
		budget = room
	}
	if budget < minThinkingBudget {
		return 0, false
	}
	return budget, true
}

// anthropicThinking is the thinking field for a request, or nil for one
// that should not carry it.
//
// Unset and off both send nothing. They differ in intent — one has no
// opinion, the other wants the least — and on this API they cannot
// differ in effect: there is no "think less than your default" to ask
// for, only an absence, and inventing a zero budget would be a request
// the API rejects rather than a smaller one.
func anthropicThinking(model string, e Effort, maxTokens int) *anthThinking {
	budget, ok := thinkingBudgets[e]
	if !ok {
		return nil
	}
	if AnthropicAdaptiveThinking(model) {
		// The newest families decide for themselves how much a request is
		// worth, and reject a budget. Every level maps to the same field
		// here, which is the honest answer: the wire has one switch, not
		// three positions, and pretending otherwise would be localcode
		// inventing a distinction the API does not have.
		return &anthThinking{Type: "adaptive"}
	}
	budget, fits := fitBudget(budget, maxTokens)
	if !fits {
		return nil
	}
	return &anthThinking{Type: "enabled", BudgetTokens: budget}
}

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
	// The API takes system as an array of blocks, so the assembly's
	// source-distinct blocks travel as themselves: one API block per
	// prompt asset, in order. The fold to one string is the fallback for
	// a request that arrived without blocks, not the preferred shape.
	var system any
	switch {
	case len(req.SystemBlocks) > 0:
		blocks := make([]anthContentBlock, len(req.SystemBlocks))
		for i, b := range req.SystemBlocks {
			blocks[i] = anthContentBlock{Type: "text", Text: b.Text}
		}
		system = blocks
	case req.System != "":
		system = req.System
	}
	if req.CachePrefix {
		// Two breakpoints, at the two places the request stops being the
		// same as last time: after the tool schemas and after the system
		// prompt. Both are byte-identical from turn to turn in an agent
		// session, and together they are the bulk of every request's fixed
		// cost. On a multi-block system the mark goes on the last block,
		// which caches everything before it: one breakpoint, the whole
		// stable prefix.
		if n := len(tools); n > 0 {
			tools[n-1].CacheControl = ephemeral()
		}
		switch sys := system.(type) {
		case []anthContentBlock:
			sys[len(sys)-1].CacheControl = ephemeral()
		case string:
			if sys != "" {
				system = []anthContentBlock{{Type: "text", Text: sys, CacheControl: ephemeral()}}
			}
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
		Model:     req.Model,
		System:    system,
		Messages:  messages,
		Tools:     tools,
		MaxTokens: req.MaxTokens,
		Stream:    true,
		Thinking:  anthropicThinking(req.Model, req.Effort, req.MaxTokens),
	}
	body.Temperature = temperatureFor(req)
	if req.ToolChoice == ToolChoiceNone && len(tools) > 0 {
		body.ToolChoice = &anthToolChoice{Type: "none"}
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

		// Thinking accumulates the same way, and for one more reason: the
		// signature arrives after the text it signs, so neither half is
		// complete until the block closes.
		type pendingThinking struct {
			text      strings.Builder
			signature strings.Builder
		}
		thinkingByIndex := map[int]*pendingThinking{}

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
				if ev.ContentBlock != nil && ev.ContentBlock.Type == "thinking" {
					thinkingByIndex[ev.Index] = &pendingThinking{}
				}
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
				case "thinking_delta":
					if p, ok := thinkingByIndex[ev.Index]; ok {
						p.text.WriteString(ev.Delta.Thinking)
						if !send(StreamEvent{Type: EventThinkingDelta, ThinkingDelta: ev.Delta.Thinking}) {
							return
						}
					}
				case "signature_delta":
					// Arrives after the text, in pieces like everything
					// else. Kept rather than shown: it is the API's
					// attestation of the block, not part of it.
					if p, ok := thinkingByIndex[ev.Index]; ok {
						p.signature.WriteString(ev.Delta.Signature)
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
				if p, ok := thinkingByIndex[ev.Index]; ok {
					if !send(StreamEvent{
						Type: EventThinkingEnd, ThinkingDelta: p.text.String(), Signature: p.signature.String(),
					}) {
						return
					}
					delete(thinkingByIndex, ev.Index)
				}
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
