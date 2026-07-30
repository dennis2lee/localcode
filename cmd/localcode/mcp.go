// `localcode mcp` manages MCP server entries in config.json from the
// command line, Claude Code-style (`claude mcp add/list/get/remove`) —
// so a server can be registered without hand-editing JSON. It only edits
// the config file(s); a running daemon picks up changes on its next
// start (or reconnect), same as any other config.json edit.
package main

import "fmt"

const mcpUsage = `usage: localcode mcp <subcommand>

  localcode mcp add [-e KEY=VALUE]... [-s global|project] <name> -- <command> [args...]
                       register a local MCP server run as a child process over stdio
  localcode mcp add --transport http|sse [-H "Key: Value"]... [-s global|project] <name> <url>
                       register a remote MCP server; -H repeats, for an auth token
  localcode mcp add-json [-s global|project] <name> '<json>'
                       register directly from JSON, either {"command":...,"args":[...],"env":{...}}
                       or {"type":"http","url":...,"headers":{...}}
  localcode mcp list [--no-test]
                       one line per server: name, scope, and whether it connects.
                       --no-test lists without connecting to anything.
  localcode mcp get <name>       show one server's full definition
  localcode mcp remove [-s global|project] <name>
                       remove a server (project wins if --scope is omitted; --scope is required if the name exists in both)
  localcode mcp import-claude [-s global|project] [--skip-existing]
                       import an existing Claude Code user's MCP servers from ./.mcp.json and ~/.claude.json,
                       local and remote alike. Re-running overwrites a server already registered under the
                       same name unless --skip-existing is given.

  -s, --scope   global (default, ~/.localcode/config.json) or project (./.localcode/config.json)

Changes take effect the next time the daemon starts (or reconnects).`

func runMCP(args []string) error {
	if len(args) == 0 {
		fmt.Println(mcpUsage)
		return nil
	}
	switch args[0] {
	case "add":
		return mcpAdd(args[1:])
	case "add-json":
		return mcpAddJSON(args[1:])
	case "list", "ls":
		return mcpList(args[1:])
	case "get":
		return mcpGet(args[1:])
	case "remove", "rm":
		return mcpRemove(args[1:])
	case "import-claude":
		return mcpImportClaude(args[1:])
	case "help", "-h", "--help":
		fmt.Println(mcpUsage)
		return nil
	default:
		fmt.Println(mcpUsage)
		return fmt.Errorf("unknown mcp subcommand %q", args[0])
	}
}
