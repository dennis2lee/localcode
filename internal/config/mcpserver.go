package config

import (
	"encoding/json"
	"fmt"
	"net/url"
)

// MCP transport names. They match the "type" field Claude Code writes in
// .mcp.json / ~/.claude.json, so an entry can be copied across verbatim.
// Opencode aliases "local" (stdio) and "remote" (http) are also accepted.
const (
	MCPTransportStdio  = "stdio"
	MCPTransportHTTP   = "http"   // streamable HTTP, the current remote transport
	MCPTransportSSE    = "sse"    // the older HTTP+SSE transport, still in use
	MCPTransportLocal  = "local"  // opencode alias for stdio
	MCPTransportRemote = "remote" // opencode alias for http
)

// MCPServerConfig describes one MCP server. It accepts Claude Code's
// mcpServers entries and opencode's mcp entries. Two kinds of server share
// this struct, distinguished by Type:
//
//   - stdio: a local child process (Command, Args, Env, Cwd)
//   - http, sse: a remote endpoint (URL, Headers)
//
// Type may be omitted. Transport infers http when URL is present, stdio
// otherwise.
type MCPServerConfig struct {
	// Type is "stdio", "http", "sse", "local", or "remote". Empty means
	// "infer" (see Transport).
	Type string `json:"type,omitempty"`

	// stdio only.
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`

	// http/sse only. Headers carries secrets such as bearer tokens.
	// Display commands print only header keys, never values.
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`

	// Enabled controls whether this server is started. Nil means enabled.
	Enabled *bool `json:"enabled,omitempty"`

	// Timeout is the deadline for one request, in milliseconds. Zero
	// means none, which is what localcode has always done — see TimeoutMS
	// for why opencode's 5000 is not adopted as the default.
	Timeout int `json:"timeout,omitempty"`

	// Validation markers populated by UnmarshalJSON. They record a
	// question the JSON asked twice, which Validate answers by refusing
	// rather than by choosing one — a file that says a thing two ways is
	// a file whose author is not sure, and guessing serves nobody.
	emptyCommand bool
	bothEnv      bool
	bothArgs     bool
}

// mcpCommand unmarshals a "command" property. Opencode writes an array of
// strings ([command, arg1, arg2]). Localcode and Claude Code write a bare
// command string with args beside it.
//
// Unmarshaling into a named type avoids loosening the field to any.
type mcpCommand struct {
	value    string
	args     []string
	fromList bool
	empty    bool
}

func (c *mcpCommand) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		c.value = s
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		c.fromList = true
		if len(list) == 0 {
			c.empty = true
			return nil
		}
		c.value = list[0]
		c.args = list[1:]
		return nil
	}
	return fmt.Errorf(`"command" must be a string or an array of strings`)
}

// rawMCPServerConfig mirrors MCPServerConfig for JSON decoding. It handles
// alternate spellings and shapes from opencode before normalizing into
// MCPServerConfig.
type rawMCPServerConfig struct {
	Type        string            `json:"type,omitempty"`
	Command     mcpCommand        `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Cwd         string            `json:"cwd,omitempty"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Enabled     *bool             `json:"enabled,omitempty"`
	Timeout     *int              `json:"timeout,omitempty"`
}

// UnmarshalJSON normalizes Claude Code and opencode JSON schemas into
// MCPServerConfig. Opencode's command array is split into Command and Args.
// Opencode's "environment" map is mapped to Env. If both "env" and
// "environment" are present, bothEnv is flagged so Validate rejects it.
func (c *MCPServerConfig) UnmarshalJSON(data []byte) error {
	var raw rawMCPServerConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	c.Type = raw.Type
	c.URL = raw.URL
	c.Headers = raw.Headers
	c.Enabled = raw.Enabled
	c.Cwd = raw.Cwd
	if raw.Timeout != nil {
		c.Timeout = *raw.Timeout
	}

	if raw.Command.fromList {
		if raw.Command.empty {
			c.emptyCommand = true
		} else {
			c.Command = raw.Command.value
			c.Args = raw.Command.args
			if len(raw.Args) > 0 {
				c.bothArgs = true
			}
		}
	} else {
		c.Command = raw.Command.value
		c.Args = raw.Args
	}

	if raw.Env != nil && raw.Environment != nil {
		c.bothEnv = true
	} else if raw.Environment != nil {
		c.Env = raw.Environment
	} else {
		c.Env = raw.Env
	}

	return nil
}

// Transport resolves which transport this entry uses. An explicit Type
// wins. It accepts stdio, http, sse, and opencode aliases local (stdio)
// and remote (http).
//
// An empty Type infers http when URL is present, stdio otherwise. An
// unrecognised Type is returned as-is rather than guessed from URL presence,
// so Validate can reject typos.
func (c MCPServerConfig) Transport() string {
	switch c.Type {
	case MCPTransportStdio, MCPTransportLocal:
		return MCPTransportStdio
	case MCPTransportHTTP, MCPTransportRemote:
		return MCPTransportHTTP
	case MCPTransportSSE:
		return MCPTransportSSE
	case "":
		if c.URL != "" {
			return MCPTransportHTTP
		}
		return MCPTransportStdio
	default:
		return c.Type
	}
}

// IsRemote reports whether this server is reached over the network rather
// than started as a child process.
func (c MCPServerConfig) IsRemote() bool {
	return c.Transport() != MCPTransportStdio
}

// IsEnabled reports whether this server should be started. Nil defaults to
// true.
func (c MCPServerConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// TimeoutMS is the deadline for one request to this server, in
// milliseconds, and zero for none.
//
// Zero rather than opencode's 5000. A default deadline is a new way for a
// server that works today to start failing, and five seconds is short for
// the work an MCP tool is usually asked to do — a search, a build, a
// query against something slow. opencode can choose it because it has
// always had it; adopting it here would break installations to match a
// number. A config that asks for a deadline gets exactly the one it
// asked for.
func (c MCPServerConfig) TimeoutMS() int {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 0
}

// Validate checks that the entry carries what its transport needs, so a
// half-written config fails with a clear message instead of at connect time.
func (c MCPServerConfig) Validate() error {
	if c.bothEnv {
		return fmt.Errorf(`cannot set both "env" and "environment"`)
	}
	if c.bothArgs {
		return fmt.Errorf(`"command" is an array (opencode's spelling, arguments included) and "args" is set beside it; use one or the other`)
	}
	if c.emptyCommand {
		return fmt.Errorf(`"command" array must not be empty`)
	}
	if c.Timeout < 0 {
		return fmt.Errorf("timeout %d must not be negative", c.Timeout)
	}

	switch t := c.Transport(); t {
	case MCPTransportStdio:
		// Absolute, because there is no one directory a relative one could
		// mean here. opencode resolves it against the project; this server
		// is started by a daemon whose own working directory is wherever it
		// was launched and which can be told to work somewhere else while
		// running. Silently resolving against that would start the server
		// in a directory the file never named, so the file is asked to say
		// which one it means.
		if c.Cwd != "" && !absolutePath(c.Cwd) {
			return fmt.Errorf(`"cwd" %q must be an absolute path`, c.Cwd)
		}
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
		if c.Cwd != "" {
			return fmt.Errorf(`%s server must not set "cwd"`, t)
		}
		u, err := url.Parse(c.URL)
		if err != nil {
			return fmt.Errorf("invalid url %q: %w", c.URL, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("url %q must be http or https", c.URL)
		}
	default:
		return fmt.Errorf("unknown type %q (want stdio, http, sse, local, or remote)", c.Type)
	}
	return nil
}
