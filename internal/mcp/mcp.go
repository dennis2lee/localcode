// Package mcp connects to MCP servers configured in config.json's
// mcp_servers (same shape as Claude Code's .mcp.json mcpServers) over
// stdio, and adapts each server's tools into the tools.Tool interface so
// the agent loop can call them like any built-in tool.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"localcode/internal/config"
	"localcode/internal/tools"
)

// Manager owns the live connections to configured MCP servers so they can
// be closed together (e.g. on daemon shutdown).
type Manager struct {
	sessions []*mcpsdk.ClientSession
}

// Connect starts every configured MCP server over stdio, lists each
// server's tools, and returns both a Manager (for lifecycle/Close) and the
// flattened, namespaced tools ready to register on a tools.Registry.
func Connect(ctx context.Context, servers map[string]config.MCPServerConfig) (*Manager, []tools.Tool, error) {
	m := &Manager{}
	var out []tools.Tool

	for name, sc := range servers {
		session, err := connectOne(ctx, name, sc)
		if err != nil {
			m.Close()
			return nil, nil, err
		}
		m.sessions = append(m.sessions, session)

		result, err := session.ListTools(ctx, &mcpsdk.ListToolsParams{})
		if err != nil {
			m.Close()
			return nil, nil, fmt.Errorf("list tools for mcp server %q: %w", name, err)
		}

		for _, t := range result.Tools {
			schema, err := json.Marshal(t.InputSchema)
			if err != nil {
				m.Close()
				return nil, nil, fmt.Errorf("marshal input schema for %s/%s: %w", name, t.Name, err)
			}
			out = append(out, mcpTool{
				session:     session,
				server:      name,
				name:        t.Name,
				description: t.Description,
				inputSchema: schema,
			})
		}
	}

	return m, out, nil
}

func connectOne(ctx context.Context, name string, sc config.MCPServerConfig) (*mcpsdk.ClientSession, error) {
	cmd := exec.Command(sc.Command, sc.Args...)
	if len(sc.Env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range sc.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "localcode", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, &mcpsdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect mcp server %q (%s): %w", name, sc.Command, err)
	}
	return session, nil
}

// Close shuts down every connected MCP server session.
func (m *Manager) Close() {
	for _, s := range m.sessions {
		_ = s.Close()
	}
}

// mcpTool adapts one remote MCP tool into tools.Tool. Its name is
// namespaced as mcp__<server>__<tool>, matching Claude Code's convention,
// so same-named tools on different servers can't collide.
type mcpTool struct {
	session     *mcpsdk.ClientSession
	server      string
	name        string
	description string
	inputSchema json.RawMessage
}

func (t mcpTool) Name() string                 { return fmt.Sprintf("mcp__%s__%s", t.server, t.name) }
func (t mcpTool) Description() string          { return t.description }
func (t mcpTool) InputSchema() json.RawMessage { return t.inputSchema }

// RequiresPermission is always true. MCP tools can advertise a
// "read-only" hint, but the SDK's own docs warn clients never to make
// access-control decisions based on those (self-reported, possibly
// untrusted) hints — so every MCP call goes through the same permission
// gate as bash/write/edit.
func (t mcpTool) RequiresPermission(json.RawMessage) bool { return true }

func (t mcpTool) Execute(ctx context.Context, input json.RawMessage) tools.Result {
	var args map[string]any
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return tools.Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}
		}
	}

	result, err := t.session.CallTool(ctx, &mcpsdk.CallToolParams{Name: t.name, Arguments: args})
	if err != nil {
		return tools.Result{Content: fmt.Sprintf("mcp call failed: %v", err), IsError: true}
	}

	var text strings.Builder
	for _, c := range result.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	return tools.Result{Content: text.String(), IsError: result.IsError}
}
