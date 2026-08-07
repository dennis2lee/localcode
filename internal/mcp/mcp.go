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

	"localcode/internal/childproc"
	"localcode/internal/config"
	"localcode/internal/tools"
)

// Status is what a client's indicator shows for one server.
type Status string

const (
	// StatusConnected: the last thing we did with this server worked.
	StatusConnected Status = "connected"
	// StatusDegraded: it is configured and we have a session, but
	// something recently failed — a call errored, or a health check
	// didn't come back. Distinct from disconnected because the session
	// may well recover on the next call, and a client showing this as
	// "down" would be crying wolf.
	StatusDegraded Status = "degraded"
	// StatusDisconnected: no usable session. Either it never came up at
	// startup or it died and the reconnect failed.
	StatusDisconnected Status = "disconnected"
)

// ServerState is one server's entry in a status listing.
type ServerState struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	// Detail is the last error, when there is one — what a tooltip shows
	// so "disconnected" isn't the end of the story for someone trying to
	// fix it.
	Detail string `json:"detail,omitempty"`
}

type serverEntry struct {
	config  config.MCPServerConfig
	session *mcpsdk.ClientSession // nil once disconnected
	status  Status
	detail  string
}

// Manager owns the live connections to configured MCP servers, keyed by
// server name, so a dead one can be re-dialed without disturbing the
// others. A server that fails to connect (or list tools) at startup is
// skipped rather than aborting the whole daemon — see Connect's returned
// warnings — but it stays in the map as a known, disconnected server so
// its state is still reportable.
type Manager struct {
	mu      sync.Mutex
	servers map[string]*serverEntry

	// onChange is called (outside the lock) whenever a server's status
	// changes, and only then — the daemon turns it into an event for
	// connected clients, and a callback firing on every health check
	// instead of on every real change would be a needless wakeup every
	// interval for every client.
	onChange func([]ServerState)

	// stopHealth closes to stop the background health checker. nil when
	// none is running.
	stopHealth chan struct{}
}

func newManager() *Manager {
	return &Manager{servers: map[string]*serverEntry{}}
}

// OnStatusChange registers the callback fired when any server's status
// changes. Safe to call before or after Connect; only one is kept.
func (m *Manager) OnStatusChange(fn func([]ServerState)) {
	m.mu.Lock()
	m.onChange = fn
	m.mu.Unlock()
}

// setStatus records a server's status and reports whether it changed, so
// only real transitions reach onChange. The caller must hold mu; the
// notification itself happens outside it (see notify).
func (m *Manager) setStatus(name string, status Status, detail string) bool {
	e, ok := m.servers[name]
	if !ok {
		return false
	}
	if e.status == status && e.detail == detail {
		return false
	}
	e.status, e.detail = status, detail
	return true
}

// notify calls onChange with a fresh snapshot, from outside the lock — a
// callback that reached back into the Manager (the daemon's does not, but
// nothing stops a future one) would otherwise deadlock.
func (m *Manager) notify() {
	m.mu.Lock()
	fn := m.onChange
	m.mu.Unlock()
	if fn == nil {
		return
	}
	fn(m.States())
}

// States lists every configured server and its status, sorted by name.
// Unlike Servers() this includes servers that never came up — a server
// that is configured and dead is exactly what an indicator needs to show,
// and omitting it made a broken server look like one nobody had set up.
func (m *Manager) States() []ServerState {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.servers))
	for name := range m.servers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]ServerState, 0, len(names))
	for _, name := range names {
		e := m.servers[name]
		out = append(out, ServerState{Name: name, Status: e.status, Detail: e.detail})
	}
	return out
}

// HealthInterval is how often StartHealthChecks re-probes every server.
// Long enough that a handful of stdio servers cost nothing measurable,
// short enough that an indicator isn't lying for minutes.
const HealthInterval = 30 * time.Second

// healthTimeout bounds one probe. A server that cannot answer ListTools
// in this long is not usable for a tool call either, which is the thing
// the indicator is really reporting.
const healthTimeout = 5 * time.Second

// StartHealthChecks polls every configured server until ctx is done or
// Close is called, so a server that dies quietly is noticed.
//
// A poll is needed in addition to the failures tool calls already report:
// tool calls only happen when the model reaches for a server, so an idle
// session could sit for an hour in front of an indicator claiming a
// long-dead server was fine. Conversely the polls alone would be too
// slow to react during a turn, which is why both feed the same status.
func (m *Manager) StartHealthChecks(ctx context.Context) {
	m.mu.Lock()
	if m.stopHealth != nil {
		m.mu.Unlock()
		return // already running
	}
	stop := make(chan struct{})
	m.stopHealth = stop
	m.mu.Unlock()

	go func() {
		ticker := time.NewTicker(HealthInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				m.CheckHealth(ctx)
			}
		}
	}()
}

// CheckHealth probes every server once and notifies if anything changed.
// Exported so a test can drive it without waiting out the interval.
func (m *Manager) CheckHealth(ctx context.Context) {
	m.mu.Lock()
	names := make([]string, 0, len(m.servers))
	for name := range m.servers {
		names = append(names, name)
	}
	sort.Strings(names)
	m.mu.Unlock()

	changed := false
	for _, name := range names {
		if m.probe(ctx, name) {
			changed = true
		}
	}
	// One notification for the whole sweep, not one per server: clients
	// re-render the entire list from a snapshot anyway.
	if changed {
		m.notify()
	}
}

// probe checks one server and reports whether its status changed.
func (m *Manager) probe(ctx context.Context, name string) bool {
	session := m.session(name)
	if session == nil {
		// Already known to be down. Deliberately not re-dialed here: a
		// server that is down because its command is missing would have
		// this loop starting a doomed process every interval, forever.
		// Reconnecting is what a tool call does, on demand.
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.setStatus(name, StatusDisconnected, m.servers[name].detail)
	}

	probeCtx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	_, err := session.ListTools(probeCtx, &mcpsdk.ListToolsParams{})

	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil {
		return m.setStatus(name, StatusConnected, "")
	}
	// A closed connection is genuinely down; anything else (a timeout, a
	// protocol error) is degraded — the session may still answer the next
	// real call, and swinging the light to dead on one slow probe would
	// make it flicker for no reason.
	if errors.Is(err, mcpsdk.ErrConnectionClosed) {
		if e := m.servers[name]; e != nil {
			e.session = nil
		}
		return m.setStatus(name, StatusDisconnected, err.Error())
	}
	return m.setStatus(name, StatusDegraded, err.Error())
}

// Servers returns the names of every MCP server that currently has a
// usable session. Callers wanting the configured-but-down ones too — an
// indicator has to show those, or a broken server looks like one nobody
// set up — want States instead.
func (m *Manager) Servers() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.servers))
	for name, e := range m.servers {
		if e.session != nil {
			names = append(names, name)
		}
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
// progress, when non-nil, is called with each server's name just before
// it is connected. Connecting is the slowest part of startup and the
// part most likely to stall — a server that never answers holds up
// everything behind it — so a caller showing a startup screen wants to
// name the one it is waiting on rather than a generic "loading".
func Connect(ctx context.Context, servers map[string]config.MCPServerConfig, progress func(name string)) (*Manager, []tools.Tool, []error) {
	m := newManager()
	var out []tools.Tool
	var warnings []error

	// Sorted rather than ranged over directly: map iteration order is
	// random, which made both the startup warning order and the
	// registered-tool order vary from run to run — annoying for a stable
	// log to diff and for a test to assert against, for no benefit.
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if progress != nil {
			progress(name)
		}
		toolsFromServer, serverWarnings := m.add(ctx, name, servers[name])
		out = append(out, toolsFromServer...)
		warnings = append(warnings, serverWarnings...)
	}

	return m, out, warnings
}

// add connects one server, registers its session, and adapts its
// advertised tools, so Connect reads as a loop over servers rather than
// one long inline body. A connect/list-tools failure is one warning with
// no tools; a per-tool schema-marshal failure is its own warning and just
// that tool is skipped — both match Connect's documented per-server and
// per-tool tolerance.
func (m *Manager) add(ctx context.Context, name string, sc config.MCPServerConfig) (out []tools.Tool, warnings []error) {
	// The entry is recorded before the dial, not after it, so a server
	// that never comes up is still *known* — that is what lets States
	// report it as disconnected instead of omitting it entirely.
	m.mu.Lock()
	m.servers[name] = &serverEntry{config: sc, status: StatusDisconnected}
	m.mu.Unlock()

	session, err := connectOne(ctx, sc)
	if err != nil {
		m.mu.Lock()
		m.setStatus(name, StatusDisconnected, err.Error())
		m.mu.Unlock()
		return nil, []error{fmt.Errorf("mcp server %q: %w — skipping, its tools won't be available", name, err)}
	}

	result, err := session.ListTools(ctx, &mcpsdk.ListToolsParams{})
	if err != nil {
		_ = session.Close()
		m.mu.Lock()
		m.setStatus(name, StatusDisconnected, err.Error())
		m.mu.Unlock()
		return nil, []error{fmt.Errorf("mcp server %q: list tools: %w — skipping", name, err)}
	}

	m.mu.Lock()
	e := m.servers[name]
	e.session = session
	e.status, e.detail = StatusConnected, ""
	m.mu.Unlock()

	out = make([]tools.Tool, 0, len(result.Tools))
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
	return out, warnings
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
		// Several stdio servers at startup means several console windows on
		// the Windows desktop build without this. See internal/childproc.
		childproc.Hide(cmd)
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
	if e, ok := m.servers[server]; ok {
		return e.session
	}
	return nil
}

// markDegraded records that something went wrong with server without
// concluding it is down — a single failed call usually isn't. Reported
// separately from disconnected so a client can show "having trouble"
// rather than swinging the indicator to dead and back.
func (m *Manager) markDegraded(server string, err error) {
	m.mu.Lock()
	changed := m.setStatus(server, StatusDegraded, err.Error())
	m.mu.Unlock()
	if changed {
		m.notify()
	}
}

// reconnect re-dials server using its original config, replacing whatever
// (dead) session is on file for it. Used by mcpTool.Execute after a call
// fails with ErrConnectionClosed.
func (m *Manager) reconnect(ctx context.Context, server string) (*mcpsdk.ClientSession, error) {
	m.mu.Lock()
	e, ok := m.servers[server]
	var sc config.MCPServerConfig
	var old *mcpsdk.ClientSession
	if ok {
		sc, old = e.config, e.session
	}
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no config on file for mcp server %q", server)
	}
	if old != nil {
		_ = old.Close()
	}

	session, err := connectOne(ctx, sc)
	if err != nil {
		m.mu.Lock()
		e.session = nil
		changed := m.setStatus(server, StatusDisconnected, err.Error())
		m.mu.Unlock()
		if changed {
			m.notify()
		}
		return nil, err
	}

	m.mu.Lock()
	e.session = session
	changed := m.setStatus(server, StatusConnected, "")
	m.mu.Unlock()
	if changed {
		m.notify()
	}
	return session, nil
}

// Close shuts down every connected MCP server session and stops the
// health checker.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.stopHealth != nil {
		close(m.stopHealth)
		m.stopHealth = nil
	}
	for _, e := range m.servers {
		if e.session != nil {
			_ = e.session.Close()
			e.session = nil
		}
	}
	m.mu.Unlock()
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
		// bring it back up before giving up on this call. reconnect moves
		// the status either way, so an indicator reacts at the moment the
		// break is discovered rather than at the next health check.
		newSession, rerr := t.manager.reconnect(ctx, t.server)
		if rerr != nil {
			return tools.Result{Content: fmt.Sprintf("mcp server %q disconnected and reconnect failed: %v", t.server, rerr), IsError: true}
		}
		result, err = newSession.CallTool(ctx, &mcpsdk.CallToolParams{Name: t.name, Arguments: args})
	}
	if err != nil {
		// Not disconnected — the session answered, the call just failed —
		// so this is the degraded case, and the next successful call or
		// health check clears it.
		t.manager.markDegraded(t.server, err)
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
