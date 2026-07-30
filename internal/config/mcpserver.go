package config

import (
	"fmt"
	"net/url"
)

// MCP transport names. They match the "type" field Claude Code writes in
// .mcp.json / ~/.claude.json, so an entry can be copied across verbatim.
const (
	MCPTransportStdio = "stdio"
	MCPTransportHTTP  = "http" // streamable HTTP, the current remote transport
	MCPTransportSSE   = "sse"  // the older HTTP+SSE transport, still in use
)

// MCPServerConfig describes one MCP server, in the same shape as Claude
// Code's `mcpServers` entries so an existing .mcp.json can be copied in
// directly. Two kinds of server share this struct, distinguished by Type:
//
//   - stdio: a local child process (Command/Args/Env)
//   - http, sse: a remote endpoint (URL/Headers)
//
// Type may be omitted, in which case Transport infers it — an entry with a
// URL is remote, anything else is a local command. That's what makes a
// hand-written `{"command": "npx", ...}` from before remote support existed
// keep working untouched.
type MCPServerConfig struct {
	// Type is "stdio", "http", or "sse". Empty means "infer" — see
	// Transport.
	Type string `json:"type,omitempty"`

	// stdio only.
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`

	// http/sse only. Headers is where an API token goes, so treat its
	// values as secrets: nothing in this repo prints them, only their key
	// names (see the `mcp list` / `mcp get` output).
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Transport resolves which transport this entry uses, filling in the
// default for an entry that doesn't say. An explicit Type always wins, so a
// server can be pinned to "sse" even though "http" is what a bare URL
// infers.
func (c MCPServerConfig) Transport() string {
	switch c.Type {
	case MCPTransportStdio, MCPTransportHTTP, MCPTransportSSE:
		return c.Type
	}
	if c.URL != "" {
		// Streamable HTTP is the current spec's remote transport, so it is
		// the better guess for an entry that only carries a URL. A server
		// that still speaks the older protocol needs "type": "sse".
		return MCPTransportHTTP
	}
	return MCPTransportStdio
}

// IsRemote reports whether this server is reached over the network rather
// than started as a child process.
func (c MCPServerConfig) IsRemote() bool {
	return c.Transport() != MCPTransportStdio
}

// Validate checks that the entry carries what its transport needs, so a
// half-written config fails with a clear message instead of at connect time.
func (c MCPServerConfig) Validate() error {
	switch t := c.Transport(); t {
	case MCPTransportStdio:
		if c.Command == "" {
			return fmt.Errorf(`stdio server needs a "command"`)
		}
		if c.URL != "" {
			return fmt.Errorf(`stdio server must not set "url" (use "type": "http" or "sse" for a remote server)`)
		}
	case MCPTransportHTTP, MCPTransportSSE:
		if c.URL == "" {
			return fmt.Errorf(`%s server needs a "url"`, t)
		}
		if c.Command != "" {
			return fmt.Errorf(`%s server must not set "command"`, t)
		}
		u, err := url.Parse(c.URL)
		if err != nil {
			return fmt.Errorf("invalid url %q: %w", c.URL, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("url %q must be http or https", c.URL)
		}
	}
	return nil
}
