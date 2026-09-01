package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"localcode/internal/provider"
	"localcode/internal/tools"
)

// A tool call whose name is not quite a tool's name.
//
// The unit tests for the resolution are in internal/tools. These two are
// about the turn: that a repaired name really runs the tool and says it
// did, and that an unrepairable one comes back with something the model
// can act on instead of the dead end that produced five identical calls.

type namedFake struct {
	name string
	last json.RawMessage
}

func (f *namedFake) Name() string                            { return f.name }
func (f *namedFake) Description() string                     { return f.name }
func (f *namedFake) InputSchema() json.RawMessage            { return json.RawMessage(`{"type":"object"}`) }
func (f *namedFake) RequiresPermission(json.RawMessage) bool { return false }
func (f *namedFake) Execute(_ context.Context, in json.RawMessage) tools.Result {
	f.last = in
	return tools.Result{Content: "ran " + f.name}
}

func toolNameLoop(t *testing.T) (*Loop, *namedFake) {
	t.Helper()
	srv, _ := smartServer(t)
	t.Cleanup(srv.Close)
	loop := newSmartLoop(t, srv.URL)
	fake := &namedFake{name: "bash"}
	loop.Tools.Register(fake)
	if _, err := loop.Store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	return loop, fake
}

func TestADecoratedToolNameRunsTheToolAndSaysItDid(t *testing.T) {
	loop, fake := toolNameLoop(t)
	blocks, refused, _ := loop.runTools(context.Background(), "s1", []provider.Block{{
		Type: provider.BlockToolUse, ToolUseID: "t1",
		ToolName: "bash.command", ToolInput: json.RawMessage(`{"command":"ls"}`),
	}}, nil, 100000)

	if refused {
		t.Error("a repaired name was reported as a refusal")
	}
	if len(blocks) != 1 {
		t.Fatalf("%d results", len(blocks))
	}
	if blocks[0].IsError {
		t.Errorf("the call failed: %s", blocks[0].ToolResultContent)
	}
	// It really ran, with the arguments as written.
	if string(fake.last) != `{"command":"ls"}` {
		t.Errorf("the tool got %q", fake.last)
	}
	// And the model is told which spelling worked, so it stops producing
	// the other one.
	for _, want := range []string{`no tool "bash.command"`, `ran "bash"`, "ran bash"} {
		if !strings.Contains(blocks[0].ToolResultContent, want) {
			t.Errorf("result does not contain %q:\n%s", want, blocks[0].ToolResultContent)
		}
	}
}

func TestAnUnknownToolNameComesBackWithTheRoster(t *testing.T) {
	loop, _ := toolNameLoop(t)
	blocks, _, _ := loop.runTools(context.Background(), "s1", []provider.Block{{
		Type: provider.BlockToolUse, ToolUseID: "t1",
		ToolName: "nonesuch", ToolInput: json.RawMessage(`{}`),
	}}, []string{"bash"}, 100000)

	if len(blocks) != 1 || !blocks[0].IsError {
		t.Fatalf("blocks = %+v", blocks)
	}
	got := blocks[0].ToolResultContent
	if !strings.Contains(got, `no tool is called "nonesuch"`) {
		t.Errorf("result = %s", got)
	}
	// The part that was missing: what it may call instead.
	if !strings.Contains(got, `"bash"`) {
		t.Errorf("the roster is not in the refusal:\n%s", got)
	}
}

// The restriction still holds. A name that would repair to a tool this
// agent was not offered is refused, not repaired.
func TestARestrictedAgentCannotSpellItsWayToAToolItLacks(t *testing.T) {
	loop, fake := toolNameLoop(t)
	loop.Tools.Register(&namedFake{name: "read"})
	blocks, _, _ := loop.runTools(context.Background(), "s1", []provider.Block{{
		Type: provider.BlockToolUse, ToolUseID: "t1",
		ToolName: "bash.command", ToolInput: json.RawMessage(`{"command":"rm -rf /"}`),
	}}, []string{"read"}, 100000)

	if len(blocks) != 1 || !blocks[0].IsError {
		t.Fatalf("a restricted agent reached bash: %+v", blocks)
	}
	if fake.last != nil {
		t.Errorf("bash ran with %q", fake.last)
	}
	if strings.Contains(blocks[0].ToolResultContent, `"bash"`) {
		t.Errorf("the refusal advertised a tool this agent does not have:\n%s", blocks[0].ToolResultContent)
	}
}
