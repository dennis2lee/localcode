package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestReleaseOpencodeMCPLocalCommandArray tests that opencode's local MCP
// server definition using a command array ([executable, arg1, arg2])
// parses into Command and Args, resolves transport stdio, and passes validation.
func TestReleaseOpencodeMCPLocalCommandArray(t *testing.T) {
	input := `{"type":"local","command":["npx","-y","x"]}`
	var sc MCPServerConfig
	if err := json.Unmarshal([]byte(input), &sc); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if sc.Command != "npx" {
		t.Errorf("Command = %q, want %q", sc.Command, "npx")
	}
	wantArgs := []string{"-y", "x"}
	if len(sc.Args) != len(wantArgs) || sc.Args[0] != wantArgs[0] || sc.Args[1] != wantArgs[1] {
		t.Errorf("Args = %v, want %v", sc.Args, wantArgs)
	}
	if got := sc.Transport(); got != MCPTransportStdio {
		t.Errorf("Transport() = %q, want %q", got, MCPTransportStdio)
	}
	if sc.IsRemote() {
		t.Errorf("IsRemote() = true, want false")
	}
	if err := sc.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// TestReleaseOpencodeMCPRemoteURL tests that opencode's remote MCP server
// definition with type "remote" resolves to HTTP transport and passes validation.
func TestReleaseOpencodeMCPRemoteURL(t *testing.T) {
	input := `{"type":"remote","url":"https://example.com/mcp"}`
	var sc MCPServerConfig
	if err := json.Unmarshal([]byte(input), &sc); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if got := sc.Transport(); got != MCPTransportHTTP {
		t.Errorf("Transport() = %q, want %q", got, MCPTransportHTTP)
	}
	if !sc.IsRemote() {
		t.Errorf("IsRemote() = false, want true")
	}
	if err := sc.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// TestReleaseOpencodeMCPEnvironmentField tests that opencode's "environment"
// block is accepted as an alias for "env" so variables do not vanish.
func TestReleaseOpencodeMCPEnvironmentField(t *testing.T) {
	input := `{"command":"npx","environment":{"A":"b","KEY":"VAL"}}`
	var sc MCPServerConfig
	if err := json.Unmarshal([]byte(input), &sc); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if sc.Env == nil {
		t.Fatalf("Env is nil, want populated map")
	}
	if sc.Env["A"] != "b" || sc.Env["KEY"] != "VAL" {
		t.Errorf("Env = %+v, want A=b and KEY=VAL", sc.Env)
	}
	if err := sc.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// TestReleaseOpencodeMCPEnabledFalse tests that "enabled": false sets Enabled
// pointer and IsEnabled reports false, while omitted enabled defaults to true.
func TestReleaseOpencodeMCPEnabledFalse(t *testing.T) {
	disabledInput := `{"command":"npx","enabled":false}`
	var scDisabled MCPServerConfig
	if err := json.Unmarshal([]byte(disabledInput), &scDisabled); err != nil {
		t.Fatalf("Unmarshal disabled failed: %v", err)
	}
	if scDisabled.Enabled == nil {
		t.Fatalf("Enabled is nil, want non-nil pointer")
	}
	if *scDisabled.Enabled {
		t.Errorf("*Enabled = true, want false")
	}
	if scDisabled.IsEnabled() {
		t.Errorf("IsEnabled() = true, want false")
	}

	enabledInput := `{"command":"npx","enabled":true}`
	var scEnabled MCPServerConfig
	if err := json.Unmarshal([]byte(enabledInput), &scEnabled); err != nil {
		t.Fatalf("Unmarshal enabled failed: %v", err)
	}
	if scEnabled.Enabled == nil || !*scEnabled.Enabled {
		t.Errorf("Enabled = %v, want *true", scEnabled.Enabled)
	}
	if !scEnabled.IsEnabled() {
		t.Errorf("IsEnabled() = false, want true")
	}

	defaultInput := `{"command":"npx"}`
	var scDefault MCPServerConfig
	if err := json.Unmarshal([]byte(defaultInput), &scDefault); err != nil {
		t.Fatalf("Unmarshal default failed: %v", err)
	}
	if scDefault.Enabled != nil {
		t.Errorf("Enabled = %v, want nil", scDefault.Enabled)
	}
	if !scDefault.IsEnabled() {
		t.Errorf("IsEnabled() = false, want true by default")
	}
}

// TestReleaseOpencodeMCPUnknownTypeRefused verifies that unrecognized types
// are refused and the error message names the invalid type and lists accepted types.
func TestReleaseOpencodeMCPUnknownTypeRefused(t *testing.T) {
	cases := []struct {
		name      string
		rawJSON   string
		badType   string
		serverKey string
	}{
		{
			name:      "typo with command",
			rawJSON:   `{"mcp_servers":{"bad_stdio":{"type":"stdo","command":"echo"}}}`,
			badType:   "stdo",
			serverKey: "bad_stdio",
		},
		{
			name:      "typo with url",
			rawJSON:   `{"mcp_servers":{"bad_remote":{"type":"remtoe","url":"https://example.com"}}}`,
			badType:   "remtoe",
			serverKey: "bad_remote",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cfg Config
			if err := json.Unmarshal([]byte(tc.rawJSON), &cfg); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate() succeeded, want error for unknown type %q", tc.badType)
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.serverKey) {
				t.Errorf("error message %q does not name server %q", msg, tc.serverKey)
			}
			if !strings.Contains(msg, tc.badType) {
				t.Errorf("error message %q does not name unknown type %q", msg, tc.badType)
			}
			if !strings.Contains(msg, "want stdio, http, sse, local, or remote") {
				t.Errorf("error message %q does not list accepted types", msg)
			}
		})
	}
}

// TestReleaseOpencodeMCPBothEnvSpellingsRefused verifies that providing both
// "env" and "environment" blocks is refused and names the server.
func TestReleaseOpencodeMCPBothEnvSpellingsRefused(t *testing.T) {
	input := `{"mcp_servers":{"confused":{"command":"echo","env":{"A":"1"},"environment":{"B":"2"}}}}`
	var cfg Config
	if err := json.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() succeeded, want refusal for both env and environment")
	}
	msg := err.Error()
	if !strings.Contains(msg, "confused") {
		t.Errorf("error message %q does not name server", msg)
	}
	if !strings.Contains(msg, `cannot set both "env" and "environment"`) {
		t.Errorf("error message %q does not explain both spellings were set", msg)
	}
}

// TestReleaseOpencodeMCPEmptyCommandArrayRefused verifies that an empty
// command array ([]) is refused and names the server.
func TestReleaseOpencodeMCPEmptyCommandArrayRefused(t *testing.T) {
	input := `{"mcp_servers":{"empty_srv":{"type":"local","command":[]}}}`
	var cfg Config
	if err := json.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() succeeded, want refusal for empty command array")
	}
	msg := err.Error()
	if !strings.Contains(msg, "empty_srv") {
		t.Errorf("error message %q does not name server", msg)
	}
	if !strings.Contains(msg, `"command" array must not be empty`) {
		t.Errorf("error message %q does not explain command array is empty", msg)
	}
}

// TestReleaseOpencodeMCPCwdAndTimeout tests cwd and timeout handling across
// transports and verifies Validate checks.
func TestReleaseOpencodeMCPCwdAndTimeout(t *testing.T) {
	// Valid stdio server with cwd and timeout.
	stdioInput := `{"type":"local","command":["npx","server"],"cwd":"C:/srv/subproject","timeout":3500}`
	var scStdio MCPServerConfig
	if err := json.Unmarshal([]byte(stdioInput), &scStdio); err != nil {
		t.Fatalf("Unmarshal stdio failed: %v", err)
	}
	if scStdio.Cwd != "C:/srv/subproject" {
		t.Errorf("Cwd = %q, want %q", scStdio.Cwd, "C:/srv/subproject")
	}
	if scStdio.Timeout != 3500 {
		t.Errorf("Timeout = %d, want 3500", scStdio.Timeout)
	}
	if scStdio.TimeoutMS() != 3500 {
		t.Errorf("TimeoutMS() = %d, want 3500", scStdio.TimeoutMS())
	}
	if err := scStdio.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}

	// No timeout when the file does not ask for one, which is what every
	// MCP server in every existing config has today. Adopting opencode's
	// five-second default here would put a deadline on servers that work.
	var scDefaultTimeout MCPServerConfig
	if err := json.Unmarshal([]byte(`{"command":"echo"}`), &scDefaultTimeout); err != nil {
		t.Fatalf("Unmarshal default timeout failed: %v", err)
	}
	if scDefaultTimeout.TimeoutMS() != 0 {
		t.Errorf("TimeoutMS() = %d for a server that asked for no deadline, want 0", scDefaultTimeout.TimeoutMS())
	}

	// Remote server with timeout passes.
	remoteInput := `{"type":"remote","url":"https://example.com/mcp","timeout":8000}`
	var scRemote MCPServerConfig
	if err := json.Unmarshal([]byte(remoteInput), &scRemote); err != nil {
		t.Fatalf("Unmarshal remote failed: %v", err)
	}
	if scRemote.TimeoutMS() != 8000 {
		t.Errorf("TimeoutMS() = %d, want 8000", scRemote.TimeoutMS())
	}
	if err := scRemote.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}

	// A stdio server's cwd has to be absolute: see Validate.
	var scRelCwd MCPServerConfig
	if err := json.Unmarshal([]byte(`{"command":"echo","cwd":"./sub"}`), &scRelCwd); err != nil {
		t.Fatalf("Unmarshal relative cwd failed: %v", err)
	}
	if err := scRelCwd.Validate(); err == nil {
		t.Error(`Validate() accepted a relative "cwd", which would resolve against whatever directory the daemon is in`)
	} else if !strings.Contains(err.Error(), "./sub") {
		t.Errorf("Validate() = %v, and the message does not name the path", err)
	}

	// Remote server with cwd is refused.
	remoteWithCwd := `{"type":"remote","url":"https://example.com/mcp","cwd":"./invalid"}`
	var scRemoteWithCwd MCPServerConfig
	if err := json.Unmarshal([]byte(remoteWithCwd), &scRemoteWithCwd); err != nil {
		t.Fatalf("Unmarshal remote with cwd failed: %v", err)
	}
	if err := scRemoteWithCwd.Validate(); err == nil || !strings.Contains(err.Error(), `http server must not set "cwd"`) {
		t.Errorf("Validate() = %v, want http server must not set cwd", err)
	}

	// Negative timeout is refused.
	negativeTimeout := `{"command":"echo","timeout":-100}`
	var scNegative MCPServerConfig
	if err := json.Unmarshal([]byte(negativeTimeout), &scNegative); err != nil {
		t.Fatalf("Unmarshal negative timeout failed: %v", err)
	}
	if err := scNegative.Validate(); err == nil || !strings.Contains(err.Error(), "timeout -100 must not be negative") {
		t.Errorf("Validate() = %v, want negative timeout refused", err)
	}
}

// TestReleaseClaudeCodeRegressionGuard verifies that Claude Code style
// .mcp.json entries parse and resolve exactly as they did before.
func TestReleaseClaudeCodeRegressionGuard(t *testing.T) {
	// Stdio entry: string command, string args, env map.
	stdioJSON := `{
		"command": "npx",
		"args": ["-y", "@modelcontextprotocol/server-github"],
		"env": {"GITHUB_TOKEN": "secret_abc"}
	}`
	var scStdio MCPServerConfig
	if err := json.Unmarshal([]byte(stdioJSON), &scStdio); err != nil {
		t.Fatalf("Unmarshal stdio failed: %v", err)
	}
	if scStdio.Command != "npx" {
		t.Errorf("Command = %q, want %q", scStdio.Command, "npx")
	}
	if len(scStdio.Args) != 2 || scStdio.Args[0] != "-y" || scStdio.Args[1] != "@modelcontextprotocol/server-github" {
		t.Errorf("Args = %v, want [-y @modelcontextprotocol/server-github]", scStdio.Args)
	}
	if scStdio.Env["GITHUB_TOKEN"] != "secret_abc" {
		t.Errorf("Env = %v, want GITHUB_TOKEN=secret_abc", scStdio.Env)
	}
	if got := scStdio.Transport(); got != MCPTransportStdio {
		t.Errorf("Transport() = %q, want %q", got, MCPTransportStdio)
	}
	if scStdio.IsRemote() {
		t.Errorf("IsRemote() = true, want false")
	}
	if !scStdio.IsEnabled() {
		t.Errorf("IsEnabled() = false, want true")
	}
	if err := scStdio.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}

	// Remote HTTP entry: type http, url, headers.
	httpJSON := `{
		"type": "http",
		"url": "https://api.example.com/mcp",
		"headers": {"Authorization": "Bearer secret_xyz"}
	}`
	var scHTTP MCPServerConfig
	if err := json.Unmarshal([]byte(httpJSON), &scHTTP); err != nil {
		t.Fatalf("Unmarshal http failed: %v", err)
	}
	if scHTTP.Type != "http" {
		t.Errorf("Type = %q, want %q", scHTTP.Type, "http")
	}
	if scHTTP.URL != "https://api.example.com/mcp" {
		t.Errorf("URL = %q, want %q", scHTTP.URL, "https://api.example.com/mcp")
	}
	if scHTTP.Headers["Authorization"] != "Bearer secret_xyz" {
		t.Errorf("Headers = %v, want Authorization header", scHTTP.Headers)
	}
	if got := scHTTP.Transport(); got != MCPTransportHTTP {
		t.Errorf("Transport() = %q, want %q", got, MCPTransportHTTP)
	}
	if !scHTTP.IsRemote() {
		t.Errorf("IsRemote() = false, want true")
	}
	if err := scHTTP.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}

	// Bare URL entry: no type, infers http.
	bareURLJSON := `{
		"url": "https://api.example.com/mcp",
		"headers": {"X-Custom": "custom_val"}
	}`
	var scBare MCPServerConfig
	if err := json.Unmarshal([]byte(bareURLJSON), &scBare); err != nil {
		t.Fatalf("Unmarshal bare url failed: %v", err)
	}
	if got := scBare.Transport(); got != MCPTransportHTTP {
		t.Errorf("Transport() = %q, want %q (inferred)", got, MCPTransportHTTP)
	}
	if !scBare.IsRemote() {
		t.Errorf("IsRemote() = false, want true")
	}
	if err := scBare.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}

	// SSE entry: type sse, url.
	sseJSON := `{
		"type": "sse",
		"url": "https://api.example.com/sse"
	}`
	var scSSE MCPServerConfig
	if err := json.Unmarshal([]byte(sseJSON), &scSSE); err != nil {
		t.Fatalf("Unmarshal sse failed: %v", err)
	}
	if got := scSSE.Transport(); got != MCPTransportSSE {
		t.Errorf("Transport() = %q, want %q", got, MCPTransportSSE)
	}
	if !scSSE.IsRemote() {
		t.Errorf("IsRemote() = false, want true")
	}
	if err := scSSE.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}
