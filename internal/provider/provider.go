// Package provider abstracts over model backends (Bedrock, OpenAI-compatible
// local/remote endpoints) behind a single interface. The internal message
// format is the Anthropic content-block model (text / tool_use / tool_result
// / thinking) because it is the more expressive of the two on the wire —
// OpenAI-compat adapters translate into and out of it, not the other way
// around.
package provider

import (
	"context"
	"encoding/json"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role    Role    `json:"role"`
	Content []Block `json:"content"`
}

// Block is a tagged union over content block kinds. Exactly one of the
// typed fields is set, selected by Type.
type Block struct {
	Type BlockType `json:"type"`

	Text string `json:"text,omitempty"` // BlockText, BlockThinking

	ToolUseID string          `json:"tool_use_id,omitempty"` // BlockToolUse, BlockToolResult
	ToolName  string          `json:"tool_name,omitempty"`   // BlockToolUse
	ToolInput json.RawMessage `json:"tool_input,omitempty"`  // BlockToolUse

	ToolResultContent string `json:"tool_result_content,omitempty"` // BlockToolResult
	IsError           bool   `json:"is_error,omitempty"`            // BlockToolResult
}

type BlockType string

const (
	BlockText       BlockType = "text"
	BlockThinking   BlockType = "thinking"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
)

func TextBlock(text string) Block { return Block{Type: BlockText, Text: text} }

func ToolResultBlock(toolUseID, content string, isError bool) Block {
	return Block{Type: BlockToolResult, ToolUseID: toolUseID, ToolResultContent: content, IsError: isError}
}

// Tool describes a callable tool in JSON Schema form, provider-agnostic.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type ChatRequest struct {
	Model       string
	System      string
	Messages    []Message
	Tools       []Tool
	MaxTokens   int
	Temperature float64
}

// StreamEvent is one item from a streamed model response. Exactly one field
// is meaningful per Type.
type StreamEvent struct {
	Type StreamEventType

	TextDelta string // EventTextDelta

	ToolUseID  string          // EventToolUseStart, EventToolUseInputDelta, EventToolUseEnd
	ToolName   string          // EventToolUseStart
	InputDelta string          // EventToolUseInputDelta (partial JSON fragment)
	ToolInput  json.RawMessage // EventToolUseEnd (full accumulated input)

	StopReason string // EventMessageStop: "end_turn" | "tool_use" | "max_tokens"

	InputTokens  int // EventUsage: size of this request's system+history+tools
	OutputTokens int // EventUsage: tokens generated so far this response

	Err error // EventError
}

type StreamEventType string

const (
	EventTextDelta         StreamEventType = "text_delta"
	EventToolUseStart      StreamEventType = "tool_use_start"
	EventToolUseInputDelta StreamEventType = "tool_use_input_delta"
	EventToolUseEnd        StreamEventType = "tool_use_end"
	EventMessageStop       StreamEventType = "message_stop"
	// EventUsage reports token usage for the in-progress response. A
	// provider may emit it multiple times (e.g. once early with just
	// InputTokens known, again at the end with final OutputTokens) —
	// consumers should treat each occurrence as the latest known totals,
	// not something to sum across events.
	EventUsage StreamEventType = "usage"
	EventError StreamEventType = "error"
)

// Provider is the single seam every model backend implements. Chat streams
// events on the returned channel until the response completes (a
// message_stop or error event) and then closes it.
type Provider interface {
	Chat(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
}
