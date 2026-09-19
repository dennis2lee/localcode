package mcp

import (
	"context"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"localcode/internal/config"
)

// A server the config switched off is not started.
//
// The field parsed and was thrown away before, so "enabled": false read as
// a promise the program did not keep: the process came up, its tools were
// offered to the model, and nothing said so. This is the guard for the
// half that matters — not that the field decodes, but that a switched-off
// server has no process and contributes no tools.
func TestADisabledServerIsNotStarted(t *testing.T) {
	bin := buildEchoServer(t)
	off := false
	on := true

	servers := map[string]config.MCPServerConfig{
		"off": {Command: bin, Enabled: &off},
		"on":  {Command: bin, Enabled: &on},
		// Nil is the state every existing config is in, and it means on.
		"unsaid": {Command: bin},
	}

	m, got, warnings := Connect(context.Background(), servers, "", nil)
	defer m.Close()
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	running := map[string]bool{}
	for _, name := range m.Servers() {
		running[name] = true
	}
	if running["off"] {
		t.Error(`the server with "enabled": false was started`)
	}
	if !running["on"] || !running["unsaid"] {
		t.Errorf("servers running = %v, want both \"on\" and \"unsaid\"", m.Servers())
	}
	for _, tool := range got {
		if tool.Name() == "mcp__off__echo" {
			t.Error("a disabled server's tool was offered to the model")
		}
	}
}

// cwd reaches the process the transport will start, rather than being
// parsed and dropped.
//
// Asserted on the command the transport is built with, because that is
// where the field either arrives or does not. Asking a started server
// where it thinks it is would be the better test and this fixture cannot
// answer it — a test that writes its own marker file and then calls the
// server proves nothing about Dir at all.
func TestCwdReachesTheCommandTheTransportStarts(t *testing.T) {
	dir := t.TempDir()
	transport, _, err := transportFor(config.MCPServerConfig{Command: "echo", Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	ct, ok := transport.(*mcpsdk.CommandTransport)
	if !ok {
		t.Fatalf("stdio transport is %T, not a CommandTransport", transport)
	}
	if ct.Command.Dir != dir {
		t.Errorf("the server would be started in %q, and the config said %q", ct.Command.Dir, dir)
	}

	// And a server that named no directory is started where localcode is,
	// which is what every existing config does.
	transport, _, err = transportFor(config.MCPServerConfig{Command: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	if ct, ok := transport.(*mcpsdk.CommandTransport); !ok || ct.Command.Dir != "" {
		t.Errorf("a server with no cwd would be started in %q, want the inherited directory", ct.Command.Dir)
	}
}

// No deadline unless the config asks for one.
//
// opencode defaults this to five seconds. Adopting that number would put a
// deadline on every MCP server in every config that has never had one, and
// five seconds is short for what these tools are asked to do. The guard is
// on the default rather than on the mechanism: a server that asked for a
// deadline gets it, and one that did not is left alone.
func TestNoDeadlineUnlessTheConfigAsksForOne(t *testing.T) {
	bin := buildEchoServer(t)
	servers := map[string]config.MCPServerConfig{
		"silent": {Command: bin},
		"asked":  {Command: bin, Timeout: 3500},
	}
	m, _, warnings := Connect(context.Background(), servers, "", nil)
	defer m.Close()
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if d := m.timeoutFor("silent"); d != 0 {
		t.Errorf("a server that asked for no deadline got %v", d)
	}
	if d := m.timeoutFor("asked"); d.Milliseconds() != 3500 {
		t.Errorf("a server that asked for 3500ms got %v", d)
	}
}
