// Package mcp connects to MCP servers configured in config.json's
// mcp_servers (same shape as Claude Code's .mcp.json mcpServers) and adapts
// each server's tools into the tools.Tool interface so the agent loop can
// call them like any built-in tool.
//
// All three MCP transports are supported: a local child process over stdio,
// and two remote ones over HTTP (streamable HTTP, and the older HTTP+SSE).
// Which one an entry uses comes from its "type", or is inferred from
// whether it carries a url — see config.MCPServerConfig.Transport.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"localcode/internal/config"
	"localcode/internal/tools"
)

// Manager owns the live connections to configured MCP servers, keyed by
// server name, so a dead one can be re-dialed without disturbing the
// others. A server that fails to connect (or list tools) at startup is
// skipped rather than aborting the whole daemon — see Connect's returned
// warnings.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*mcpsdk.ClientSession
	configs  map[string]config.MCPServerConfig
}

func newManager() *Manager {
	return &Manager{
		sessions: map[string]*mcpsdk.ClientSession{},
		configs:  map[string]config.MCPServerConfig{},
	}
}

// Servers returns the names of every MCP server currently connected
// (i.e. it came up successfully at startup — see Connect's warnings for
// ones that didn't). A session that's since died is still listed here
// until the next tool call against it triggers a reconnect attempt; this
// package has no background health check.
func (m *Manager) Servers() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.sessions))
	for name := range m.sessions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Connect brings up every configured MCP server — starting a child process
// for stdio entries, dialing the endpoint for remote ones — and lists each
// server's tools. Per-server failures (bad command, connection refused,
// ListTools error) are collected as warnings and that server is skipped —
// they never prevent the daemon from starting with whatever servers *did*
// come up.
func Connect(ctx context.Context, servers map[string]config.MCPServerConfig) (*Manager, []tools.Tool, []error) {
	m := newManager()
	var out []tools.Tool
	var warnings []error

	for name, sc := range servers {
		session, err := connectOne(ctx, sc)
		if err != nil {
			warnings = append(warnings, fmt.Errorf("mcp server %q: %w — skipping, its tools won't be available", name, err))
			continue
		}

		result, err := session.ListTools(ctx, &mcpsdk.ListToolsParams{})
		if err != nil {
			warnings = append(warnings, fmt.Errorf("mcp server %q: list tools: %w — skipping", name, err))
			_ = session.Close()
			continue
		}

		m.mu.Lock()
		m.sessions[name] = session
		m.configs[name] = sc
		m.mu.Unlock()

		for _, t := range result.Tools {
			schema, err := json.Marshal(t.InputSchema)
			if err != nil {
				warnings = append(warnings, fmt.Errorf("mcp server %q: marshal schema for tool %q: %w — skipping that tool", name, t.Name, err))
				continue
			}
			out = append(out, mcpTool{
				manager:     m,
				server:      name,
				name:        t.Name,
				description: t.Description,
				inputSchema: schema,
			})
		}
	}

	return m, out, warnings
}

// Ping starts one configured MCP server, completes the MCP handshake, lists
// its tools, and shuts it back down, returning the tool names it advertised.
// It is a connectivity check for `localcode mcp list` — the config file on
// its own can only say a server is *registered*, never that its command
// exists, starts, and speaks MCP.
//
// The server process is started and killed for real, so a server with
// expensive startup work pays that cost here; callers should bound this with
// a context deadline.
func Ping(ctx context.Context, sc config.MCPServerConfig) ([]string, error) {
	session, err := connectOne(ctx, sc)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	result, err := session.ListTools(ctx, &mcpsdk.ListToolsParams{})
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	names := make([]string, 0, len(result.Tools))
	for _, t := range result.Tools {
		names = append(names, t.Name)
	}
	sort.Strings(names)
	return names, nil
}

func connectOne(ctx context.Context, sc config.MCPServerConfig) (*mcpsdk.ClientSession, error) {
	transport, target, err := transportFor(sc)
	if err != nil {
		return nil, err
	}

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "localcode", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect (%s): %w", target, err)
	}
	return session, nil
}

// transportFor builds the SDK transport for one server entry, plus a short
// description of what it is connecting to for error messages. The three
// transports are the ones Claude Code writes into its own config, so an
// imported entry works without being rewritten.
func transportFor(sc config.MCPServerConfig) (mcpsdk.Transport, string, error) {
	switch t := sc.Transport(); t {
	case config.MCPTransportStdio:
		cmd := exec.Command(sc.Command, sc.Args...)
		if len(sc.Env) > 0 {
			cmd.Env = os.Environ()
			for k, v := range sc.Env {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
		}
		return &mcpsdk.CommandTransport{Command: cmd}, sc.Command, nil

	case config.MCPTransportHTTP:
		return &mcpsdk.StreamableClientTransport{
			Endpoint:   sc.URL,
			HTTPClient: httpClientFor(sc.Headers),
		}, sc.URL, nil

	case config.MCPTransportSSE:
		return &mcpsdk.SSEClientTransport{
			Endpoint:   sc.URL,
			HTTPClient: httpClientFor(sc.Headers),
		}, sc.URL, nil

	default:
		return nil, "", fmt.Errorf("unknown transport %q (want stdio, http, or sse)", t)
	}
}

// responseHeaderTimeout bounds how long a remote MCP server may take to
// start answering a request.
//
// Deliberately NOT http.Client.Timeout: that one covers reading the
// response body too, and both remote transports hold a response open
// indefinitely — the SSE transport's event stream, and the streamable
// transport's standalone GET stream for server-initiated messages. A
// whole-request deadline would tear those down on a timer. Bounding only
// the wait for response *headers* catches a server that accepts the
// connection and then says nothing, while leaving a healthy stream alone.
const responseHeaderTimeout = 60 * time.Second

func httpClientFor(headers map[string]string) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.ResponseHeaderTimeout = responseHeaderTimeout

	if len(headers) == 0 {
		return &http.Client{Transport: base}
	}
	// Copy, so a later config mutation can't change what an already-open
	// connection sends.
	cp := make(map[string]string, len(headers))
	for k, v := range headers {
		cp[k] = v
	}
	return &http.Client{Transport: headerTransport{headers: cp, base: base}}
}

// headerTransport adds the configured headers to every request — how an API
// token reaches a remote MCP server, since the SDK's transports expose no
// header field of their own.
//
// Headers are set only when absent, so a header the SDK itself needs to
// control (Accept, Content-Type, the session id) is never clobbered by a
// config entry.
type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// RoundTrippers must not modify the request they're given.
	clone := req.Clone(req.Context())
	for k, v := range t.headers {
		if clone.Header.Get(k) == "" {
			clone.Header.Set(k, v)
		}
	}
	return t.base.RoundTrip(clone)
}

// session returns the currently live session for server, if any.
func (m *Manager) session(server string) *mcpsdk.ClientSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[server]
}

// reconnect re-dials server using its original config, replacing whatever
// (dead) session is on file for it. Used by mcpTool.Execute after a call
// fails with ErrConnectionClosed.
func (m *Manager) reconnect(ctx context.Context, server string) (*mcpsdk.ClientSession, error) {
	m.mu.Lock()
	sc, ok := m.configs[server]
	old := m.sessions[server]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no config on file for mcp server %q", server)
	}
	if old != nil {
		_ = old.Close()
	}

	session, err := connectOne(ctx, sc)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.sessions[server] = session
	m.mu.Unlock()
	return session, nil
}

// Close shuts down every connected MCP server session.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		_ = s.Close()
	}
}

// mcpTool adapts one remote MCP tool into tools.Tool. Its name is
// namespaced as mcp__<server>__<tool>, matching Claude Code's convention,
// so same-named tools on different servers can't collide. It looks up the
// live session through manager at call time (rather than holding one
// directly) so a reconnect transparently takes effect on the next call.
type mcpTool struct {
	manager     *Manager
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

	session := t.manager.session(t.server)
	if session == nil {
		return tools.Result{Content: fmt.Sprintf("mcp server %q is not connected", t.server), IsError: true}
	}

	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: t.name, Arguments: args})
	if err != nil && errors.Is(err, mcpsdk.ErrConnectionClosed) {
		// The server process likely died or closed the pipe; try once to
		// bring it back up before giving up on this call.
		newSession, rerr := t.manager.reconnect(ctx, t.server)
		if rerr != nil {
			return tools.Result{Content: fmt.Sprintf("mcp server %q disconnected and reconnect failed: %v", t.server, rerr), IsError: true}
		}
		result, err = newSession.CallTool(ctx, &mcpsdk.CallToolParams{Name: t.name, Arguments: args})
	}
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
