package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

	m, tools, warnings := Connect(ctx, servers, nil)
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

	m, _, warnings := Connect(ctx, servers, nil)
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
	m, _, _ := Connect(context.Background(), nil, nil)
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

	m, tools, warnings := Connect(ctx, servers, nil)
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
	m, tools, warnings := Connect(ctx, servers, nil)
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
	m, tools, warnings := Connect(ctx, servers, nil)
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

// A server that never comes up must still be reported — as disconnected,
// with the reason. Leaving it out of the listing made a broken server
// indistinguishable from one nobody had configured, which is the opposite
// of what a status indicator is for.
func TestStatesIncludeServersThatNeverConnected(t *testing.T) {
	m, tools, warnings := Connect(context.Background(), map[string]config.MCPServerConfig{
		"broken": {Command: "definitely-not-a-real-command-localcode-test"},
	}, nil)
	defer m.Close()

	if len(tools) != 0 {
		t.Errorf("expected no tools from a server that failed to start, got %d", len(tools))
	}
	if len(warnings) == 0 {
		t.Error("expected a warning for the failed server")
	}

	states := m.States()
	if len(states) != 1 {
		t.Fatalf("States() = %+v, want one entry for the configured-but-dead server", states)
	}
	if states[0].Name != "broken" || states[0].Status != StatusDisconnected {
		t.Errorf("States()[0] = %+v, want broken/disconnected", states[0])
	}
	if states[0].Detail == "" {
		t.Error("expected a detail explaining why it is down")
	}
	// Servers() is the has-a-session list and must NOT include it.
	if got := m.Servers(); len(got) != 0 {
		t.Errorf("Servers() = %v, want empty — it never connected", got)
	}
}

// The callback is the daemon's hook for turning a change into an event.
// It must fire on a real transition and stay quiet otherwise, or every
// health check would wake every connected client for nothing.
func TestStatusChangeCallbackFiresOnlyOnRealChanges(t *testing.T) {
	m, _, _ := Connect(context.Background(), map[string]config.MCPServerConfig{
		"broken": {Command: "definitely-not-a-real-command-localcode-test"},
	}, nil)
	defer m.Close()

	var mu sync.Mutex
	var calls [][]ServerState
	m.OnStatusChange(func(states []ServerState) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, states)
	})

	// Already disconnected, and a probe of a session-less server does not
	// re-dial (that would restart a doomed process every interval), so
	// nothing changes and nothing fires.
	m.CheckHealth(context.Background())
	mu.Lock()
	n := len(calls)
	mu.Unlock()
	if n != 0 {
		t.Errorf("callback fired %d times with no status change, want 0", n)
	}

	// A real transition does fire, with the whole list.
	m.markDegraded("broken", errors.New("something went wrong"))
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("callback fired %d times after a real change, want 1", len(calls))
	}
	if len(calls[0]) != 1 || calls[0][0].Status != StatusDegraded {
		t.Errorf("callback got %+v, want one degraded entry", calls[0])
	}
}

// Auth headers are added by the RoundTripper on every hop, which is after
// Go has decided whether a redirect is crossing to a different host — so
// they were put back on the request to that host. A remote MCP server (or
// anyone able to answer for it) could collect the bearer token by
// replying `307 Location: https://elsewhere/`.
//
// "localhost" and "127.0.0.1" are different hosts to that check, which is
// what makes this a leak rather than an ordinary same-host hop.
func TestConfiguredHeadersDoNotFollowARedirectToAnotherHost(t *testing.T) {
	received := make(chan string, 2)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Get("Authorization")
	}))
	defer other.Close()
	otherURL := strings.Replace(other.URL, "127.0.0.1", "localhost", 1)

	configured := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, otherURL+"/collect", http.StatusTemporaryRedirect)
	}))
	defer configured.Close()

	resp, err := httpClientFor(map[string]string{"Authorization": "Bearer SECRET"}).Get(configured.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()

	if got := <-received; got != "" {
		t.Errorf("the other host received Authorization: %q", got)
	}
}

// The header must still reach the server it was configured for, including
// across a redirect that stays on that host — otherwise this fix would
// break every authenticated server that redirects at all.
func TestConfiguredHeadersSurviveASameHostRedirect(t *testing.T) {
	received := make(chan string, 2)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved" {
			received <- r.Header.Get("Authorization")
			return
		}
		http.Redirect(w, r, srv.URL+"/moved", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()

	resp, err := httpClientFor(map[string]string{"Authorization": "Bearer SECRET"}).Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()

	if got := <-received; got != "Bearer SECRET" {
		t.Errorf("same-host redirect lost the header: %q", got)
	}
}
