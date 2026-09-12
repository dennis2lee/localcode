package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"localcode/internal/tools"
)

// Turning one MCP server off for one conversation.
//
// The daemon could list the servers and reconnect them, and could not
// turn one off. The only way was editing config.json and running
// "/reset-mcp", which stops it for every conversation on the machine.

// namedTool is a registered tool that does nothing. Only its name
// matters here: what is under test is which names are offered.
type namedTool string

func (t namedTool) Name() string                          { return string(t) }
func (namedTool) Description() string                     { return "a stub" }
func (namedTool) InputSchema() json.RawMessage            { return json.RawMessage(`{"type":"object"}`) }
func (namedTool) RequiresPermission(json.RawMessage) bool { return false }
func (namedTool) Execute(context.Context, json.RawMessage) tools.Result {
	return tools.Result{Content: "ok"}
}

// mcpLoop is a loop with two servers' tools registered and a states hook
// that names them.
func mcpLoop(t *testing.T) (*Loop, string) {
	t.Helper()
	loop, sid, _ := effortLoop(t, "")
	loop.MCPStates = func() []IntegrationState {
		return []IntegrationState{
			{Name: "github", Status: "connected"},
			{Name: "noisy", Status: "connected"},
		}
	}
	reg := tools.NewRegistry(func(context.Context, tools.Ask) (bool, error) { return true, nil })
	for _, name := range []string{"mcp__github__issues", "mcp__github__prs", "mcp__noisy__ping"} {
		reg.Register(namedTool(name))
	}
	loop.Tools = reg
	return loop, sid
}

func TestMCPsListsEveryServerAndWhetherItIsUsedHere(t *testing.T) {
	loop, sid := mcpLoop(t)

	if err := loop.SendMessage(context.Background(), sid, "boy", "/mcps"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	reply := lastReply(t, loop, sid)
	for _, want := range []string{"github", "noisy", "connected", "2 tools", "usage: /mcps"} {
		if !strings.Contains(reply, want) {
			t.Errorf("/mcps does not report %q: %s", want, reply)
		}
	}
}

// Off hides that server's tools from the next request here, and says so
// — "off" reads like stopping the server, and it does not.
func TestTurningAServerOffHidesItsToolsInThisConversationOnly(t *testing.T) {
	loop, sid := mcpLoop(t)

	if err := loop.SendMessage(context.Background(), sid, "boy", "/mcps off noisy"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	reply := lastReply(t, loop, sid)
	for _, want := range []string{"noisy: off", "stays connected", "off here"} {
		if !strings.Contains(reply, want) {
			t.Errorf("the reply does not say %q: %s", want, reply)
		}
	}

	ctx := WithSessionID(context.Background(), sid)
	hidden := loop.hiddenTools(ctx)
	if !hidden["mcp__noisy__ping"] {
		t.Error("the server was turned off and its tool is still offered")
	}
	if hidden["mcp__github__issues"] {
		t.Error("turning one server off hid another server's tools")
	}

	// Another conversation is untouched: this is per conversation for the
	// reason the permission switches are.
	if _, err := loop.Store.CreateSession("elsewhere", "", "boy", true); err != nil {
		t.Fatal(err)
	}
	if loop.hiddenTools(WithSessionID(context.Background(), "elsewhere"))["mcp__noisy__ping"] {
		t.Error("turning a server off in one conversation turned it off in another")
	}

	// And back on again.
	if err := loop.SendMessage(context.Background(), sid, "boy", "/mcps on noisy"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if loop.hiddenTools(ctx)["mcp__noisy__ping"] {
		t.Error("/mcps on did not put the tools back")
	}
}

// A name that is not a server is refused by name, with the list, because
// nobody memorises what their MCP servers are called.
func TestAnUnknownServerIsRefusedWithTheList(t *testing.T) {
	loop, sid := mcpLoop(t)

	if err := loop.SendMessage(context.Background(), sid, "boy", "/mcps off nope"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	reply := lastReply(t, loop, sid)
	for _, want := range []string{`no MCP server called "nope"`, "github", "noisy"} {
		if !strings.Contains(reply, want) {
			t.Errorf("the refusal does not mention %q: %s", want, reply)
		}
	}
	// And the usage line when the server is left off entirely.
	if err := loop.SendMessage(context.Background(), sid, "boy", "/mcps off"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if reply := lastReply(t, loop, sid); !strings.Contains(reply, "usage: /mcps off <server>") {
		t.Errorf("no usage line: %s", reply)
	}
}

// With nothing configured it says so rather than printing an empty list.
func TestMCPsWithNoServers(t *testing.T) {
	loop, sid, _ := effortLoop(t, "")
	if err := loop.SendMessage(context.Background(), sid, "boy", "/mcps"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if reply := lastReply(t, loop, sid); !strings.Contains(reply, "No MCP servers are configured") {
		t.Errorf("/mcps with none configured: %s", reply)
	}
}
