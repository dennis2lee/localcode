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

	m, tools, err := Connect(ctx, servers)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer m.Close()

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

func TestConnectUnknownCommand(t *testing.T) {
	ctx := context.Background()
	servers := map[string]config.MCPServerConfig{
		"broken": {Command: "this-binary-does-not-exist-anywhere"},
	}
	_, _, err := Connect(ctx, servers)
	if err == nil {
		t.Fatal("expected an error connecting to a nonexistent command")
	}
}
