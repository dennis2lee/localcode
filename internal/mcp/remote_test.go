package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"localcode/internal/config"
)

// startRemoteServer builds and runs the testdata/httpserver fixture and
// returns the URL it bound. Everything here talks to a real HTTP server
// speaking real MCP — the point is to exercise the SDK's remote transports
// and our header plumbing, which a fake transport would not.
func startRemoteServer(t *testing.T, args ...string) string {
	t.Helper()

	bin := filepath.Join(t.TempDir(), "httpserver")
	build := exec.Command("go", "build", "-o", bin, "./testdata/httpserver")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build httpserver fixture: %v\n%s", err, out)
	}

	cmd := exec.Command(bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start httpserver: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// The fixture prints its URL as soon as it is listening, which avoids
	// racing a fixed sleep against a slow machine.
	line := make(chan string, 1)
	go func() {
		r := bufio.NewReader(stdout)
		s, _ := r.ReadString('\n')
		line <- strings.TrimSpace(s)
	}()
	select {
	case url := <-line:
		if url == "" {
			t.Fatal("httpserver fixture printed no url")
		}
		return url
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for the httpserver fixture to start")
		return ""
	}
}

// TestConnectStreamableHTTP is the case that makes an imported Claude Code
// remote server work: an entry with a url and no command connects over
// streamable HTTP and its tools become callable.
func TestConnectStreamableHTTP(t *testing.T) {
	url := startRemoteServer(t)
	ctx := context.Background()

	servers := map[string]config.MCPServerConfig{
		"remote": {Type: config.MCPTransportHTTP, URL: url},
	}
	m, toolList, warnings := Connect(ctx, servers, nil)
	defer m.Close()
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(toolList) != 1 {
		t.Fatalf("got %d tools, want 1", len(toolList))
	}
	if got := toolList[0].Name(); got != "mcp__remote__echo" {
		t.Errorf("tool name = %q, want mcp__remote__echo — remote tools are namespaced like stdio ones", got)
	}

	result := toolList[0].Execute(ctx, json.RawMessage(`{"text":"hi"}`))
	if result.IsError {
		t.Fatalf("call failed: %s", result.Content)
	}
	if !strings.Contains(result.Content, "echo: hi") {
		t.Errorf("result = %q, want it to contain %q", result.Content, "echo: hi")
	}
}

// TestConnectSSE covers the older HTTP+SSE transport, which is still what a
// good number of hosted MCP servers speak, and which an entry only reaches
// by setting "type": "sse" explicitly.
func TestConnectSSE(t *testing.T) {
	url := startRemoteServer(t, "--transport", "sse")
	ctx := context.Background()

	servers := map[string]config.MCPServerConfig{
		"remote": {Type: config.MCPTransportSSE, URL: url},
	}
	m, toolList, warnings := Connect(ctx, servers, nil)
	defer m.Close()
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(toolList) != 1 {
		t.Fatalf("got %d tools, want 1", len(toolList))
	}

	result := toolList[0].Execute(ctx, json.RawMessage(`{"text":"over sse"}`))
	if result.IsError {
		t.Fatalf("call failed: %s", result.Content)
	}
	if !strings.Contains(result.Content, "echo: over sse") {
		t.Errorf("result = %q, want it to contain %q", result.Content, "echo: over sse")
	}
}

// TestRemoteHeadersAreSent pins the whole point of the headers field: a
// server that rejects unauthenticated requests is reachable with the header
// configured and not without it. Without this, a wrong header would look
// exactly like a server being down.
func TestRemoteHeadersAreSent(t *testing.T) {
	url := startRemoteServer(t, "--require-header", "Authorization:Bearer secret-token")
	ctx := context.Background()

	withHeader := config.MCPServerConfig{
		Type:    config.MCPTransportHTTP,
		URL:     url,
		Headers: map[string]string{"Authorization": "Bearer secret-token"},
	}
	names, err := Ping(ctx, withHeader)
	if err != nil {
		t.Fatalf("Ping with the configured header failed: %v", err)
	}
	if len(names) != 1 || names[0] != "echo" {
		t.Errorf("tools = %v, want [echo]", names)
	}

	withoutHeader := config.MCPServerConfig{Type: config.MCPTransportHTTP, URL: url}
	if _, err := Ping(ctx, withoutHeader); err == nil {
		t.Error("Ping without the header succeeded, so the header can't be what let the first one through")
	}

	wrongHeader := config.MCPServerConfig{
		Type:    config.MCPTransportHTTP,
		URL:     url,
		Headers: map[string]string{"Authorization": "Bearer wrong"},
	}
	if _, err := Ping(ctx, wrongHeader); err == nil {
		t.Error("Ping with a wrong token succeeded")
	}
}

// TestHeaderTransportDoesNotClobberSDKHeaders pins that a config entry
// cannot break the protocol by overriding a header the SDK sets itself
// (Content-Type, Accept, the session id).
func TestHeaderTransportDoesNotClobberSDKHeaders(t *testing.T) {
	client := httpClientFor(map[string]string{
		"Content-Type":  "text/plain",
		"Authorization": "Bearer t",
	})
	rt, ok := client.Transport.(headerTransport)
	if !ok {
		t.Fatalf("transport = %T, want headerTransport", client.Transport)
	}

	captured := &captureRoundTripper{}
	rt.base = captured

	req, err := newRequestWithHeader("Content-Type", "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}

	if got := captured.header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want the caller's %q left intact", got, "application/json")
	}
	if got := captured.header.Get("Authorization"); got != "Bearer t" {
		t.Errorf("Authorization = %q, want the configured header added", got)
	}
	// The RoundTripper contract: the request it was handed is not modified.
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("the original request was mutated (Authorization = %q)", got)
	}
}

// captureRoundTripper records the headers it was called with and returns a
// canned empty response.
type captureRoundTripper struct{ header http.Header }

func (c *captureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c.header = req.Header.Clone()
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     http.Header{},
		Request:    req,
	}, nil
}

func newRequestWithHeader(key, value string) (*http.Request, error) {
	req, err := http.NewRequest(http.MethodPost, "http://example.invalid/mcp", strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	req.Header.Set(key, value)
	return req, nil
}
