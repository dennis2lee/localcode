package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"localcode/internal/config"
)

const mcpAddUsage = `usage:
  localcode mcp add [-e KEY=VALUE]... [-s global|project] <name> -- <command> [args...]
  localcode mcp add --transport http|sse [-H "Key: Value"]... [-s global|project] <name> <url>`

func mcpAdd(args []string) error {
	var name, scope, transport string
	env := map[string]string{}
	headers := map[string]string{}

	idx := 0
loop:
	for idx < len(args) {
		a := args[idx]
		switch {
		case a == "-e" || a == "--env":
			if idx+1 >= len(args) {
				return fmt.Errorf("--env requires a KEY=VALUE argument")
			}
			k, v, ok := strings.Cut(args[idx+1], "=")
			if !ok {
				return fmt.Errorf("--env value %q must be KEY=VALUE", args[idx+1])
			}
			env[k] = v
			idx += 2
		case a == "-H" || a == "--header":
			if idx+1 >= len(args) {
				return fmt.Errorf(`--header requires a "Key: Value" argument`)
			}
			k, v, ok := strings.Cut(args[idx+1], ":")
			if !ok {
				return fmt.Errorf(`--header value %q must be "Key: Value"`, args[idx+1])
			}
			headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
			idx += 2
		case a == "-t" || a == "--transport":
			if idx+1 >= len(args) {
				return fmt.Errorf("--transport requires an argument (stdio|http|sse)")
			}
			transport = args[idx+1]
			idx += 2
		case a == "-s" || a == "--scope":
			if idx+1 >= len(args) {
				return fmt.Errorf("--scope requires an argument (global|project)")
			}
			scope = args[idx+1]
			idx += 2
		case a == "--":
			idx++
			break loop
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q", a)
		case name == "":
			name = a
			idx++
		default:
			break loop
		}
	}
	rest := args[idx:]

	if name == "" {
		return fmt.Errorf("%s", mcpAddUsage)
	}

	var sc config.MCPServerConfig
	switch transport {
	case config.MCPTransportHTTP, config.MCPTransportSSE:
		if len(rest) != 1 {
			return fmt.Errorf("a %s server takes exactly one argument, its url, e.g.: localcode mcp add --transport %s %s https://example.com/mcp", transport, transport, name)
		}
		if len(env) > 0 {
			return fmt.Errorf("--env applies to stdio servers only; use --header for a remote server's credentials")
		}
		sc = config.MCPServerConfig{Type: transport, URL: rest[0], Headers: headers}

	case "", config.MCPTransportStdio:
		if len(rest) == 0 {
			return fmt.Errorf("missing command to run the MCP server, e.g.: localcode mcp add %s -- npx -y @modelcontextprotocol/server-github\n(for a remote server: localcode mcp add --transport http %s https://example.com/mcp)", name, name)
		}
		if len(headers) > 0 {
			return fmt.Errorf("--header applies to remote servers only; use --env for a stdio server's environment")
		}
		// Left as "" rather than written out, so a stdio entry keeps the
		// same shape it has always had in config.json.
		sc = config.MCPServerConfig{Command: rest[0], Args: rest[1:], Env: env}

	default:
		return fmt.Errorf("unknown transport %q (want stdio, http, or sse)", transport)
	}

	if err := sc.Validate(); err != nil {
		return err
	}

	path, err := resolveScopePath(scope)
	if err != nil {
		return err
	}
	overwrote, err := saveMCPServer(path, name, sc)
	if err != nil {
		return err
	}
	if overwrote {
		fmt.Printf("mcp server %q already exists in %s — overwriting\n", name, path)
	}
	fmt.Printf("Added MCP server %q (%s scope) to %s\n  %s\n", name, scopeLabel(scope), path, formatMCPCommand(sc))
	return nil
}

func mcpAddJSON(args []string) error {
	scope, positional, err := parseScope(args)
	if err != nil {
		return err
	}
	if len(positional) < 2 {
		return fmt.Errorf(`usage: localcode mcp add-json [-s global|project] <name> '{"command":"...","args":[...],"env":{...}}'`)
	}
	name, jsonStr := positional[0], positional[1]

	var sc config.MCPServerConfig
	if err := json.Unmarshal([]byte(jsonStr), &sc); err != nil {
		return fmt.Errorf("parse server json: %w", err)
	}
	if err := sc.Validate(); err != nil {
		return fmt.Errorf("server json: %w", err)
	}

	path, err := resolveScopePath(scope)
	if err != nil {
		return err
	}
	overwrote, err := saveMCPServer(path, name, sc)
	if err != nil {
		return err
	}
	if overwrote {
		fmt.Printf("mcp server %q already exists in %s — overwriting\n", name, path)
	}
	fmt.Printf("Added MCP server %q (%s scope) to %s\n  %s\n", name, scopeLabel(scope), path, formatMCPCommand(sc))
	return nil
}

// saveMCPServer writes name->sc into the config at path and reports
// whether an existing entry was overwritten — decided inside the update
// callback so the check and the write are one atomic read-modify-write,
// but *printed* by the caller, after the write has actually succeeded (the
// original version printed the warning from inside the callback, so it
// fired even on a subsequent write failure).
func saveMCPServer(path, name string, sc config.MCPServerConfig) (overwrote bool, err error) {
	err = config.UpdateMCPServersInFile(path, func(servers map[string]config.MCPServerConfig) error {
		_, overwrote = servers[name]
		servers[name] = sc
		return nil
	})
	return overwrote, err
}
