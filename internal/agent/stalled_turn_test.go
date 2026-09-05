package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// scriptedProvider replays a fixed list of streams, one per turn, so a test
// can say exactly what the server reports — including the shapes a local
// server produces that the real API never does.
type scriptedProvider struct {
	// mu guards everything below. One provider now serves turns that
	// genuinely run at once — two scheduled tasks in two conversations
	// firing at the same moment — and an unguarded counter and slice
	// under -race is a failure in the harness reported as a failure in
	// the code it is testing.
	mu    sync.Mutex
	turns [][]provider.StreamEvent
	sent  int
	// systems records the system prompt of each request, which is how the
	// per-workspace rules tests see what the model was actually told.
	systems []string
	// requests is every request as it went out, for the tests that ask
	// about something other than the system prompt — whether the tools
	// were callable, for one.
	requests []provider.ChatRequest
}

// sentCount and systemPrompts read what the harness recorded, under the
// same lock the recording takes. Tests read these after the turns they
// are about have finished, but "after" is not a thing the race detector
// can see.
func (p *scriptedProvider) sentCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sent
}

func (p *scriptedProvider) systemPrompts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.systems...)
}

func (p *scriptedProvider) sentRequests() []provider.ChatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]provider.ChatRequest(nil), p.requests...)
}

func (p *scriptedProvider) Chat(ctx context.Context, req provider.ChatRequest) (<-chan provider.StreamEvent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.systems = append(p.systems, req.System)
	p.requests = append(p.requests, req)
	var evs []provider.StreamEvent
	if p.sent < len(p.turns) {
		evs = p.turns[p.sent]
	}
	p.sent++
	ch := make(chan provider.StreamEvent, len(evs)+1)
	for _, ev := range evs {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

// echoTool records that it ran, which is the whole question here.
type echoTool struct{ ran *bool }

func (t echoTool) Name() string        { return "echo" }
func (t echoTool) Description() string { return "echo" }
func (t echoTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t echoTool) RequiresPermission(input json.RawMessage) bool { return false }
func (t echoTool) Execute(ctx context.Context, input json.RawMessage) tools.Result {
	*t.ran = true
	return tools.Result{Content: "ok"}
}

func scriptedLoop(t *testing.T, p provider.Provider, reg *tools.Registry) (*Loop, string) {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	const sessionID = "s-1"
	if _, err := store.CreateSession(sessionID, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1"}},
		Profiles:       map[string]config.Profile{"balanced": {Provider: "local", Model: "m"}},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "balanced"}},
		DefaultProfile: "balanced",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	return New(store, reg, map[string]provider.Provider{"local": p}, cfg), sessionID
}

func toolCallStream(stopReason string) []provider.StreamEvent {
	return []provider.StreamEvent{
		{Type: provider.EventTextDelta, TextDelta: "running the command now"},
		{Type: provider.EventToolUseStart, ToolUseID: "call_1", ToolName: "echo"},
		{Type: provider.EventToolUseEnd, ToolUseID: "call_1", ToolInput: json.RawMessage(`{}`)},
		{Type: provider.EventMessageStop, StopReason: stopReason},
	}
}

// A reply that asked for a tool has to be answered by running it, even when
// the server reports it stopped for some other reason. Local servers say
// "stop" alongside tool calls routinely; taking that at face value ended
// the turn with the call unrun — the model announced what it was about to
// do and then, from the outside, simply stopped.
func TestToolsRunEvenWhenTheServerSaysTheTurnEnded(t *testing.T) {
	ran := false
	reg := tools.NewRegistry(nil)
	reg.Register(echoTool{ran: &ran})

	p := &scriptedProvider{turns: [][]provider.StreamEvent{
		toolCallStream("end_turn"),
		{{Type: provider.EventTextDelta, TextDelta: "done"}, {Type: provider.EventMessageStop, StopReason: "end_turn"}},
	}}
	loop, sessionID := scriptedLoop(t, p, reg)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if !ran {
		t.Fatal("the tool the model asked for never ran; the turn ended instead")
	}
	if p.sentCount() != 2 {
		t.Errorf("provider turns = %d, want 2 (the tool result has to go back to the model)", p.sentCount())
	}
}

// The exception: a reply cut off by max_tokens stops mid-write, so the
// arguments of a tool call at the end of it are truncated. Running that
// means acting on half an instruction, so the turn ends and says why.
func TestATruncatedReplyDoesNotRunItsToolCall(t *testing.T) {
	ran := false
	reg := tools.NewRegistry(nil)
	reg.Register(echoTool{ran: &ran})

	p := &scriptedProvider{turns: [][]provider.StreamEvent{toolCallStream("max_tokens")}}
	loop, sessionID := scriptedLoop(t, p, reg)

	if err := loop.SendMessage(context.Background(), sessionID, "general-purpose", "do it"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if ran {
		t.Error("a tool call from a reply cut off by max_tokens was run anyway")
	}
	evs, err := loop.Store.Events(sessionID, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	explained := false
	for _, ev := range evs {
		if ev.Type == events.TypeError {
			explained = true
		}
	}
	if !explained {
		t.Error("nothing in the transcript says the reply was cut off")
	}
}
