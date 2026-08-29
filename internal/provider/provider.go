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

	// Signature is the provider's attestation of a thinking block, and it
	// travels with the text because the text is worthless without it: an
	// API that produced reasoning refuses a continuation whose thinking
	// block is not the one it signed. Empty for every other block kind.
	Signature string `json:"signature,omitempty"` // BlockThinking

	ToolUseID string          `json:"tool_use_id,omitempty"` // BlockToolUse, BlockToolResult
	ToolName  string          `json:"tool_name,omitempty"`   // BlockToolUse
	ToolInput json.RawMessage `json:"tool_input,omitempty"`  // BlockToolUse

	ToolResultContent string `json:"tool_result_content,omitempty"` // BlockToolResult
	IsError           bool   `json:"is_error,omitempty"`            // BlockToolResult

	// Source is the prompt-entry ID for a block whose author the message
	// role does not express: a skill body or a command expansion sent as
	// the user's turn, a runtime notice localcode wrote into a user-role
	// message. Wire formats do not carry it, exactly as SystemBlock.Asset
	// is not carried; it is how the assembly manifest can name every
	// source in the request rather than only the ones created during the
	// call that happens to be running.
	//
	// Tool results need no Source: their author is derivable from the
	// tool_use block they answer, which is in the same history. The one
	// exception is a result carrying material somebody else wrote,
	// which Sources names, because no pairing can recover it.
	Source  string        `json:"source,omitempty"`
	Sources []BlockSource `json:"sources,omitempty"`
}

// BlockSource is one contributor's material inside a block's text: the
// prompt-entry ID it belongs to, and the span of the text it occupies.
// Wire formats do not carry it, exactly as Source is not carried.
type BlockSource struct {
	ID   string `json:"id"`
	From int    `json:"from"`
	To   int    `json:"to"`
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

// SystemBlock is one source-distinct piece of the system prompt: the
// rendering of one prompt asset, still knowing which one.
//
// It exists so the distinction between sources survives to the adapter
// instead of dying in a fold. The Anthropic API takes system as an array
// of blocks and Bedrock takes a list of SystemContentBlocks, so on those
// backends the request on the wire keeps the same seams the assembly
// had; an adapter whose protocol takes one string folds at the last
// possible moment, and that fold is recorded as a lowering in the
// assembly manifest rather than happening invisibly here.
type SystemBlock struct {
	Text string
	// Asset is the prompt-asset ID this block rendered from. Wire
	// formats do not carry it; the manifest is where it joins the
	// request, and this field is what keeps adapter tests able to say
	// which block was which.
	Asset string
}

type ChatRequest struct {
	Model string
	// System is the folded system prompt: every block joined in order.
	// The compatibility form, and also what sizing arithmetic measures.
	System string
	// SystemBlocks is the same content with its seams intact, one block
	// per prompt asset in assembly order. When non-empty, an adapter
	// with a native multi-block system field sends these as separate
	// blocks; an adapter without one uses System and the fold is on the
	// record. Invariant: System equals the blocks joined with blank
	// lines, so the two forms cannot disagree about content.
	SystemBlocks []SystemBlock
	Messages     []Message
	Tools        []Tool
	MaxTokens    int
	Temperature  float64

	// CachePrefix asks the backend to mark prompt-cache breakpoints,
	// where it has them to mark. Two go at the end of the stable part —
	// the tool schemas and the system prompt, byte-identical from turn
	// to turn and the largest fixed cost in an agent request — and up to
	// two more move with the conversation, on the last blocks of the
	// last two messages. The history is append-only, so each request
	// reads the previous one's marked prefix at the cache rate and
	// writes only its own new suffix at the premium.
	//
	// A request rather than a guarantee. Providers ignore a breakpoint on
	// a prefix shorter than their minimum (1024 tokens on most Claude
	// models), a local OpenAI-compatible server does its own prefix
	// caching with nothing to declare, and Bedrock only honours it on
	// some models. Nothing fails when it is not honoured; the request is
	// simply priced as it was before.
	CachePrefix bool

	// Effort asks the model to spend more or less of its own reasoning on
	// this request. Empty says nothing at all, which is the default and
	// leaves every provider's request byte-identical to what it was.
	//
	// One word here, several wires under it: an OpenAI-compatible server
	// takes "reasoning_effort", and Anthropic's API takes a thinking
	// block whose shape depends on the model's age. What a level means is
	// therefore per provider and, on one of them, per model family — see
	// each adapter. localcode's part is to carry the intent, not to
	// pretend the wires agree.
	Effort Effort
}

// Effort is how hard the model is asked to think. Off is not the same as
// unset: unset says nothing and leaves the model's own default alone,
// while off asks for the least the wire can express.
type Effort string

const (
	EffortUnset  Effort = ""
	EffortOff    Effort = "off"
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
)

// Levels is every effort a person may configure, in order, for a message
// that has to list them.
func Levels() []Effort { return []Effort{EffortOff, EffortLow, EffortMedium, EffortHigh} }

// ValidEffort reports whether s names a level. The empty string does not:
// callers that mean "unset" have it already and do not need to ask.
func ValidEffort(s string) bool {
	for _, e := range Levels() {
		if string(e) == s {
			return true
		}
	}
	return false
}

// StreamEvent is one item from a streamed model response. Exactly one field
// is meaningful per Type.
type StreamEvent struct {
	Type StreamEventType

	TextDelta string // EventTextDelta

	// ThinkingDelta is reasoning the model is doing rather than the
	// answer it is giving. Separate from TextDelta because it is not the
	// reply: appending it to the answer would put the model's working
	// where its conclusion goes.
	ThinkingDelta string // EventThinkingDelta
	// Signature arrives at the end of a thinking block. See Block.
	Signature string // EventThinkingEnd

	ToolUseID  string          // EventToolUseStart, EventToolUseInputDelta, EventToolUseEnd
	ToolName   string          // EventToolUseStart
	InputDelta string          // EventToolUseInputDelta (partial JSON fragment)
	ToolInput  json.RawMessage // EventToolUseEnd (full accumulated input)

	StopReason string // EventMessageStop: "end_turn" | "tool_use" | "max_tokens"

	InputTokens  int // EventUsage: size of this request's system+history+tools
	OutputTokens int // EventUsage: tokens generated so far this response
	// Cache accounting, where the provider reports it. Read tokens were
	// served from a previous request's cached prefix and are billed at a
	// fraction of the input rate; write tokens were put into the cache by
	// this request and are billed at a premium. Both are zero on a
	// provider that says nothing, which is not the same as "no caching
	// happened" and is why they are reported separately from InputTokens
	// rather than folded into it.
	CacheReadTokens  int // EventUsage
	CacheWriteTokens int // EventUsage

	Err error // EventError
}

type StreamEventType string

const (
	EventTextDelta StreamEventType = "text_delta"
	// EventThinkingDelta and EventThinkingEnd bracket one block of the
	// model's reasoning. The end carries the signature, which has to go
	// back with the block on the next request of the same turn.
	EventThinkingDelta     StreamEventType = "thinking_delta"
	EventThinkingEnd       StreamEventType = "thinking_end"
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
