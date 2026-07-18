package mcp

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"

	"localcode/internal/config"
)

// buildEchoServer compiles the testdata/echoserver fixture once per test
// run into a temp binary, so Connect() can be exercised against a real
// stdio subprocess speaking actual MCP JSON-RPC, not an in-process mock.
func buildEchoServer(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "echoserver")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/echoserver")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build echoserver fixture: %v\n%s", err, out)
	}
	return bin
}

func TestConnectAndCallTool(t *testing.T) {
	bin := buildEchoServer(t)
	ctx := context.Background()

	servers := map[string]config.MCPServerConfig{
		"echo": {Command: bin},
	}

	m, tools, warnings := Connect(ctx, servers)
	defer m.Close()
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	if len(tools) != 1 {
		t.Fatalf("expected 1 tool from echoserver, got %d: %+v", len(tools), tools)
	}

	tool := tools[0]
	if got, want := tool.Name(), "mcp__echo__echo"; got != want {
		t.Errorf("tool name = %q, want %q", got, want)
	}
	if tool.RequiresPermission(nil) != true {
		t.Error("expected MCP tools to always require permission")
	}

	input, _ := json.Marshal(map[string]string{"text": "hello"})
	result := tool.Execute(ctx, input)
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", result.Content)
	}
	if want := "echo: hello"; result.Content != want {
		t.Errorf("result = %q, want %q", result.Content, want)
	}
}

// TestServersListsConnectedNames confirms Manager.Servers() reports
// exactly the servers that came up successfully — sorted, and excluding
// ones that failed to connect (those only show up in Connect's warnings).
func TestServersListsConnectedNames(t *testing.T) {
	bin := buildEchoServer(t)
	ctx := context.Background()

	servers := map[string]config.MCPServerConfig{
		"zzz-echo": {Command: bin},
		"aaa-echo": {Command: bin},
		"broken":   {Command: "this-binary-does-not-exist-anywhere"},
	}

	m, _, warnings := Connect(ctx, servers)
	defer m.Close()
	if len(warnings) != 1 {
		t.Fatalf("expected exactly 1 warning for the broken server, got %d: %v", len(warnings), warnings)
	}

	got := m.Servers()
	want := []string{"aaa-echo", "zzz-echo"}
	if len(got) != len(want) {
		t.Fatalf("Servers() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Servers()[%d] = %q, want %q (sorted)", i, got[i], want[i])
		}
	}
}

func TestServersEmptyWhenNoneConfigured(t *testing.T) {
	m, _, _ := Connect(context.Background(), nil)
	defer m.Close()
	if got := m.Servers(); len(got) != 0 {
		t.Errorf("Servers() = %v, want empty", got)
	}
}

// TestConnectPartialFailure confirms one bad server doesn't stop the
// others from connecting: a nonexistent command should show up as a
// warning, not prevent the working echo server's tools from being
// returned.
func TestConnectPartialFailure(t *testing.T) {
	bin := buildEchoServer(t)
	ctx := context.Background()

	servers := map[string]config.MCPServerConfig{
		"echo":   {Command: bin},
		"broken": {Command: "this-binary-does-not-exist-anywhere"},
	}

	m, tools, warnings := Connect(ctx, servers)
	defer m.Close()

	if len(warnings) != 1 {
		t.Fatalf("expected exactly 1 warning for the broken server, got %d: %v", len(warnings), warnings)
	}
	if len(tools) != 1 || tools[0].Name() != "mcp__echo__echo" {
		t.Fatalf("expected the echo server's tool despite the broken one, got %+v", tools)
	}
}

func TestConnectUnknownCommand(t *testing.T) {
	ctx := context.Background()
	servers := map[string]config.MCPServerConfig{
		"broken": {Command: "this-binary-does-not-exist-anywhere"},
	}
	m, tools, warnings := Connect(ctx, servers)
	defer m.Close()

	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(warnings), warnings)
	}
	if len(tools) != 0 {
		t.Fatalf("expected no tools from a server that never connected, got %+v", tools)
	}
}

// TestReconnectOnClosedConnection confirms a tool call against a session
// whose underlying process died gets one automatic reconnect attempt
// rather than failing outright.
func TestReconnectOnClosedConnection(t *testing.T) {
	bin := buildEchoServer(t)
	ctx := context.Background()

	servers := map[string]config.MCPServerConfig{
		"echo": {Command: bin},
	}
	m, tools, warnings := Connect(ctx, servers)
	defer m.Close()
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	tool := tools[0]

	// Kill the underlying session out from under the tool to simulate the
	// server process dying, without going through Manager.Close (which
	// would also be a legitimate shutdown, not a crash).
	dead := m.session("echo")
	if dead == nil {
		t.Fatal("expected a live session for \"echo\"")
	}
	if err := dead.Close(); err != nil {
		t.Fatalf("close session: %v", err)
	}

	input, _ := json.Marshal(map[string]string{"text": "after reconnect"})
	result := tool.Execute(ctx, input)
	if result.IsError {
		t.Fatalf("expected the call to succeed after an automatic reconnect, got error: %s", result.Content)
	}
	if want := "echo: after reconnect"; result.Content != want {
		t.Errorf("result = %q, want %q", result.Content, want)
	}

	// The manager should now be holding a different (new) session.
	if m.session("echo") == dead {
		t.Error("expected Manager to have replaced the dead session with a reconnected one")
	}
}
