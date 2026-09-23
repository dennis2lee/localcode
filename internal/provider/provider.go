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
	"fmt"
	"strings"
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

	// MediaType and Data carry one image. An image is bytes plus the
	// type that says how to read them, and neither is worth anything
	// without the other, so they travel together the way a thinking
	// block's text travels with its signature. Raw bytes rather than
	// base64 text: two of the three adapters want base64 on the wire and
	// one wants the bytes, so the decoded form is the one all three can
	// reach without a round trip, and the session log encodes them once
	// on its own terms.
	MediaType string `json:"media_type,omitempty"` // BlockImage
	Data      []byte `json:"data,omitempty"`       // BlockImage
	IsError   bool   `json:"is_error,omitempty"`   // BlockToolResult

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
	BlockImage      BlockType = "image"
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

	// TopP and TopK are the rest of the sampling family, nil when the
	// profile did not ask. See config.Profile for what each reaches on
	// which backend, and why they are pointers.
	TopP *float64
	TopK *int

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

	// ToolChoice constrains what the model may do with the tools on the
	// request. Empty is the default and puts nothing on the wire.
	//
	// ToolChoiceNone keeps the tool definitions in the request — so a
	// server's prefix cache still holds, which on a local model is the
	// difference between a one-second answer and a re-read of the whole
	// conversation — while telling the model it may not call one. It
	// exists for the one request keep_going makes (see
	// internal/agent/keep_going.go). Best effort: an adapter with no wire
	// for it sends nothing, and the caller treats a reply that calls a
	// tool anyway as the model's own decision to carry on.
	ToolChoice string
}

// ToolChoiceNone is the one ToolChoice value: the tools stay defined
// and the model may not call any of them.
const ToolChoiceNone = "none"

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
	// EffortXHigh is one step past high, and only some models have that
	// step. Muse reads "Reasoning strength: xhigh" from its system prompt
	// and its publisher asks for it on coding work. On the OpenAI wire it
	// is sent as high, the top of that vocabulary; on Anthropic's it is
	// the high budget, or adaptive where the model decides for itself.
	EffortXHigh Effort = "xhigh"
)

// Levels is every effort a person may configure, in order, for a message
// that has to list them.
func Levels() []Effort {
	return []Effort{EffortOff, EffortLow, EffortMedium, EffortHigh, EffortXHigh}
}

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
// readError is what a stream's read failing means once the stream has
// ended: the error, or nothing when the turn was cancelled.
//
// A cancelled turn closes the connection, the read fails with the
// cancel, and the readers offered that as a stream error. Their send
// selects between the event channel and the context's done channel,
// both ready by then, so about a third of cancels arrived as errors:
// the reply was closed as failed, an error line was drawn, and the reply
// the other two thirds kept was dropped. Pressing Esc had a coin flip's
// outcome.
func readError(ctx context.Context, err error) error {
	if err == nil || ctx.Err() != nil {
		return nil
	}
	return err
}

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

// ImageBlock is one image in a message, for a caller that has the bytes
// and the media type and should not have to know the field names.
func ImageBlock(mediaType string, data []byte) Block {
	return Block{Type: BlockImage, MediaType: mediaType, Data: data}
}

// ImageBytes is the block's image data, empty for every other kind.
func (b Block) ImageBytes() []byte { return b.Data }

// MaxImageBytesPerMessage caps the images in one message, added together
// rather than counted one at a time: three images under the limit are
// still one request, and it is the request a model has to accept.
const MaxImageBytesPerMessage = 10 * 1024 * 1024

// SupportedImageMediaType reports whether all three adapters can carry
// this type. A function of the media type alone, so the table below is a
// table and not three fixtures.
//
// The intersection, not the union: an image localcode accepts has to be
// sendable wherever the conversation goes next, and a model switch
// mid-conversation must not turn history into something its own provider
// cannot express.
func SupportedImageMediaType(mediaType string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

// ValidateMessageImages refuses a message whose images cannot be sent:
// a type no adapter carries, or more bytes than one message may hold.
func ValidateMessageImages(m Message) error {
	var total int
	for _, b := range m.Content {
		if b.Type != BlockImage {
			continue
		}
		if !SupportedImageMediaType(b.MediaType) {
			return fmt.Errorf("unsupported image media type %q: only PNG, JPEG, GIF, and WEBP are supported", b.MediaType)
		}
		total += len(b.Data)
	}
	if total > MaxImageBytesPerMessage {
		return fmt.Errorf("images in one message total %d bytes, over the 10MB limit (%d bytes)",
			total, MaxImageBytesPerMessage)
	}
	return nil
}

// ValidateRequestImages checks every message before any adapter builds a
// wire form, so the refusal names the limit rather than arriving as a
// provider's own complaint about a body it could not parse.
func ValidateRequestImages(req ChatRequest) error {
	for _, m := range req.Messages {
		if err := ValidateMessageImages(m); err != nil {
			return err
		}
	}
	return nil
}

// visionRefusalPhrases are fragments an endpoint uses when it will not
// take an image. Quoted from what the three backends actually answer
// rather than remembered: an OpenAI-compatible server without vision
// answers 400 naming the content type it did not expect, Anthropic names
// the block type, and Bedrock's validation names the field.
var visionRefusalPhrases = []string{
	"image", "vision", "multimodal", "image_url", "content type",
	// A local server without vision often never says "image" at all: it
	// rejects the shape instead, because a text-only endpoint declares
	// content as a string and an image turns it into an array of blocks.
	// That complaint is the refusal, in the only words such a server has.
	"expected a string", "must be a string", "should be a string", "array of objects",
}

// notVisionRefusal are the failures that can carry an image-shaped word
// and mean something else entirely. Checked first, because a rate limit
// on a request that happened to carry an image is a rate limit.
var notVisionRefusal = []string{
	"rate limit", "rate_limit", "too many requests", "throttling",
	"context length", "context_length", "context window", "maximum context",
	"token limit exceeded",
	"canceled", "cancelled", "deadline exceeded",
	"unauthorized", "access denied", "accessdenied", "expired token",
	"credentials",
}

// isVisionRefusal reports whether err means the model would not take an
// image. A function of the error alone -- no network, no model -- so a
// test drives it with any error value.
//
// Which models have vision is not knowable from here: a list is wrong the
// day a new model ships, and what a Bedrock account allows is not in the
// SDK or this repository. So localcode sends the image and reads the
// refusal, the way reasoningRejected reads a rejected thinking parameter
// rather than predicting which accounts accept one.
func isVisionRefusal(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	for _, p := range notVisionRefusal {
		if strings.Contains(lower, p) {
			return false
		}
	}
	for _, p := range visionRefusalPhrases {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// wrapVisionRefusal turns an endpoint's refusal into the sentence
// somebody can act on: which model would not take the image, and what to
// do instead. Every other error passes through untouched.
func wrapVisionRefusal(err error, model string) error {
	if !isVisionRefusal(err) {
		return err
	}
	return fmt.Errorf("%w\n\nhint: %s appears not to accept images. Switch to a model with vision using "+
		"\"/model\", or send the message without the image", err, model)
}
