// `localcode mcp` manages MCP server entries in config.json from the
// command line, Claude Code-style (`claude mcp add/list/get/remove`) —
// so a server can be registered without hand-editing JSON. It only edits
// the config file(s); a running daemon picks up changes on its next
// start (or reconnect), same as any other config.json edit.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"localcode/internal/config"
	mcpclient "localcode/internal/mcp"
)

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

// resolveScopePath maps a --scope value to the config file it edits.
// "" defaults to global, matching config.json's own precedence docs
// (project overrides global, but global is the more common place to
// register a server you always want available).
func resolveScopePath(scope string) (string, error) {
	switch scope {
	case "", "global", "user":
		return config.DefaultGlobalPath()
	case "project", "local":
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".localcode", "config.json"), nil
	default:
		return "", fmt.Errorf("unknown scope %q (want \"global\" or \"project\")", scope)
	}
}

func scopeLabel(scope string) string {
	switch scope {
	case "project", "local":
		return "project"
	default:
		return "global"
	}
}

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
	if err := config.UpdateMCPServersInFile(path, func(servers map[string]config.MCPServerConfig) {
		if _, exists := servers[name]; exists {
			fmt.Printf("mcp server %q already exists in %s — overwriting\n", name, path)
		}
		servers[name] = sc
	}); err != nil {
		return err
	}
	fmt.Printf("Added MCP server %q (%s scope) to %s\n  %s\n", name, scopeLabel(scope), path, formatMCPCommand(sc))
	return nil
}

func mcpAddJSON(args []string) error {
	var scope string
	var positional []string

	idx := 0
	for idx < len(args) {
		a := args[idx]
		if a == "-s" || a == "--scope" {
			if idx+1 >= len(args) {
				return fmt.Errorf("--scope requires an argument (global|project)")
			}
			scope = args[idx+1]
			idx += 2
			continue
		}
		positional = append(positional, a)
		idx++
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
	if err := config.UpdateMCPServersInFile(path, func(servers map[string]config.MCPServerConfig) {
		if _, exists := servers[name]; exists {
			fmt.Printf("mcp server %q already exists in %s — overwriting\n", name, path)
		}
		servers[name] = sc
	}); err != nil {
		return err
	}
	fmt.Printf("Added MCP server %q (%s scope) to %s\n  %s\n", name, scopeLabel(scope), path, formatMCPCommand(sc))
	return nil
}

// loadBothScopes reads the global and project config files independently
// (not merged) so callers can report which scope a server actually lives
// in, and their paths.
func loadBothScopes() (global, project *config.Config, globalPath, projectPath string, err error) {
	globalPath, err = config.DefaultGlobalPath()
	if err != nil {
		return nil, nil, "", "", err
	}
	global, err = config.LoadFile(globalPath)
	if err != nil {
		return nil, nil, "", "", err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, "", "", err
	}
	projectPath = filepath.Join(cwd, ".localcode", "config.json")
	project, err = config.LoadFile(projectPath)
	if err != nil {
		return nil, nil, "", "", err
	}
	return global, project, globalPath, projectPath, nil
}

// mcpPingTimeout bounds one server's connectivity check in `mcp list`. It
// covers process start plus the MCP handshake and a tools/list round trip;
// servers that fetch something over the network at startup (an `npx` package
// that isn't cached yet, say) are the slow case this has to leave room for.
const mcpPingTimeout = 20 * time.Second

func mcpList(args []string) error {
	test := true
	for _, a := range args {
		switch a {
		case "--no-test", "--no-connect":
			test = false
		default:
			return fmt.Errorf("unknown argument %q (usage: localcode mcp list [--no-test])", a)
		}
	}

	global, project, _, _, err := loadBothScopes()
	if err != nil {
		return err
	}
	if len(global.MCPServers) == 0 && len(project.MCPServers) == 0 {
		fmt.Println("No MCP servers registered. Add one with `localcode mcp add`.")
		return nil
	}

	names := map[string]bool{}
	for n := range global.MCPServers {
		names[n] = true
	}
	for n := range project.MCPServers {
		names[n] = true
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	// One line per server: name, where it is registered, and whether it
	// answers. The command line, url, env/header keys, and the config file
	// path are all deliberately absent — this is a status view, and
	// `mcp get <name>` is the place that shows a server's full definition.
	width := 0
	for _, n := range sorted {
		if len(n) > width {
			width = len(n)
		}
	}

	failures := 0
	for _, n := range sorted {
		sc, inProject := project.MCPServers[n]
		scope := "global"
		if inProject {
			scope = "project"
		} else {
			sc = global.MCPServers[n]
		}

		if !test {
			fmt.Printf("%-*s  %s\n", width, n, scope)
			continue
		}
		ok, status := checkMCPServer(sc)
		if !ok {
			failures++
		}
		fmt.Printf("%-*s  %-8s  %s\n", width, n, scope, status)
	}

	if test && failures > 0 {
		// Reported, not returned as an error: the listing itself succeeded,
		// and a server being down is information about that server rather
		// than a failure of the command the user ran.
		fmt.Printf("\n%d of %d server(s) failed to connect.\n", failures, len(sorted))
	}
	return nil
}

// checkMCPServer brings one server up for real and returns whether it
// answered, plus a short status phrase for the listing. Registration in
// config.json says nothing about whether the command exists, the endpoint is
// reachable, or either speaks MCP — which is the whole point of connecting
// rather than trusting the file.
func checkMCPServer(sc config.MCPServerConfig) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), mcpPingTimeout)
	defer cancel()

	toolNames, err := mcpclient.Ping(ctx, sc)
	if err != nil {
		return false, "failed: " + oneLine(err.Error())
	}
	if len(toolNames) == 0 {
		return true, "ok (no tools advertised)"
	}
	return true, fmt.Sprintf("ok (%d tool%s)", len(toolNames), plural(len(toolNames)))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// oneLine flattens an error onto a single line, since a connection failure
// can carry a multi-line body from the server and each entry here gets
// exactly one row.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	const max = 100
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}

func mcpGet(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: localcode mcp get <name>")
	}
	name := args[0]

	global, project, globalPath, projectPath, err := loadBothScopes()
	if err != nil {
		return err
	}

	if sc, ok := project.MCPServers[name]; ok {
		printMCPServerDetail(name, "project", projectPath, sc)
		if _, alsoGlobal := global.MCPServers[name]; alsoGlobal {
			fmt.Println("  (a global setting also exists, but the project setting takes priority)")
		}
		return nil
	}
	if sc, ok := global.MCPServers[name]; ok {
		printMCPServerDetail(name, "global", globalPath, sc)
		return nil
	}
	return fmt.Errorf("mcp server %q not found", name)
}

func printMCPServerDetail(name, scope, path string, sc config.MCPServerConfig) {
	fmt.Printf("%s  [%s]  (%s)\n", name, scope, path)
	fmt.Printf("  transport: %s\n", sc.Transport())
	if sc.IsRemote() {
		fmt.Printf("  url:       %s\n", sc.URL)
		// Names only. A header value is almost always a bearer token, and
		// this output gets pasted into issues and chat windows.
		if keys := sortedKeys(sc.Headers); len(keys) > 0 {
			fmt.Printf("  headers:   %s (values hidden)\n", strings.Join(keys, ", "))
		}
		return
	}
	fmt.Printf("  command:   %s\n", sc.Command)
	if len(sc.Args) > 0 {
		fmt.Printf("  args:      %s\n", strings.Join(sc.Args, " "))
	}
	for _, k := range sortedKeys(sc.Env) {
		fmt.Printf("  env:       %s=%s\n", k, sc.Env[k])
	}
}

func mcpRemove(args []string) error {
	var scope string
	var positional []string

	idx := 0
	for idx < len(args) {
		a := args[idx]
		if a == "-s" || a == "--scope" {
			if idx+1 >= len(args) {
				return fmt.Errorf("--scope requires an argument (global|project)")
			}
			scope = args[idx+1]
			idx += 2
			continue
		}
		positional = append(positional, a)
		idx++
	}
	if len(positional) < 1 {
		return fmt.Errorf("usage: localcode mcp remove [-s global|project] <name>")
	}
	name := positional[0]

	if scope != "" {
		path, err := resolveScopePath(scope)
		if err != nil {
			return err
		}
		if err := removeMCPServerFromFile(path, name); err != nil {
			return err
		}
		fmt.Printf("Removed MCP server %q from %s (%s)\n", name, path, scopeLabel(scope))
		return nil
	}

	global, project, globalPath, projectPath, err := loadBothScopes()
	if err != nil {
		return err
	}
	_, inGlobal := global.MCPServers[name]
	_, inProject := project.MCPServers[name]

	switch {
	case inGlobal && inProject:
		return fmt.Errorf("mcp server %q exists in both global and project config — specify --scope global or --scope project", name)
	case inProject:
		if err := removeMCPServerFromFile(projectPath, name); err != nil {
			return err
		}
		fmt.Printf("Removed MCP server %q from %s (project)\n", name, projectPath)
	case inGlobal:
		if err := removeMCPServerFromFile(globalPath, name); err != nil {
			return err
		}
		fmt.Printf("Removed MCP server %q from %s (global)\n", name, globalPath)
	default:
		return fmt.Errorf("mcp server %q not found", name)
	}
	return nil
}

// removeMCPServerFromFile checks existence first so a not-found name
// doesn't rewrite (reformat) the file as a side effect.
func removeMCPServerFromFile(path, name string) error {
	cfg, err := config.LoadFile(path)
	if err != nil {
		return err
	}
	if _, ok := cfg.MCPServers[name]; !ok {
		return fmt.Errorf("mcp server %q not found in %s", name, path)
	}
	return config.UpdateMCPServersInFile(path, func(servers map[string]config.MCPServerConfig) {
		delete(servers, name)
	})
}

// claudeMCPEntry is one entry of a Claude Code mcpServers map — both
// ~/.claude.json (global and per-project) and a shared ./.mcp.json use
// this shape. A remote server carries URL (plus Headers, and usually a
// Type of "http" or "sse") instead of Command; localcode's own
// MCPServerConfig has the same fields, so either kind imports directly.
type claudeMCPEntry struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// claudeUserConfig is the relevant slice of ~/.claude.json: a top-level
// mcpServers map (servers available everywhere) plus a per-project block
// keyed by absolute project path (servers only registered for that one
// project, e.g. via `claude mcp add --scope project`).
type claudeUserConfig struct {
	MCPServers map[string]claudeMCPEntry `json:"mcpServers"`
	Projects   map[string]struct {
		MCPServers map[string]claudeMCPEntry `json:"mcpServers"`
	} `json:"projects"`
}

// claudeProjectMCPFile is a shared ./.mcp.json: just a top-level
// mcpServers map, meant to be checked into the repo.
type claudeProjectMCPFile struct {
	MCPServers map[string]claudeMCPEntry `json:"mcpServers"`
}

// collectClaudeMCPServers gathers every MCP server Claude Code knows about
// for the current directory — local (stdio) and remote (http/sse) alike:
// this project's checked-in ./.mcp.json, plus ~/.claude.json's global
// servers and its per-project block for cwd. Later sources win on a name
// collision (project-scoped Claude entries are the most specific, so
// they're read last). Returns the merged servers, the source files that
// actually contributed something, and any entries that couldn't be
// imported at all, each with the reason.
func collectClaudeMCPServers(cwd, home string) (servers map[string]config.MCPServerConfig, sources []string, unusable []string) {
	servers = map[string]config.MCPServerConfig{}

	addEntries := func(entries map[string]claudeMCPEntry) bool {
		contributed := false
		for name, e := range entries {
			sc := config.MCPServerConfig{
				Type:    e.Type,
				Command: e.Command,
				Args:    e.Args,
				Env:     e.Env,
				URL:     e.URL,
				Headers: e.Headers,
			}
			// An entry that says neither how to start a process nor where
			// to connect is not something this build can guess at — record
			// it by name so the import reports it instead of dropping it.
			if err := sc.Validate(); err != nil {
				unusable = append(unusable, fmt.Sprintf("%s (%v)", name, err))
				continue
			}
			servers[name] = sc
			contributed = true
		}
		return contributed
	}

	mcpJSONPath := filepath.Join(cwd, ".mcp.json")
	if data, err := os.ReadFile(mcpJSONPath); err == nil {
		var f claudeProjectMCPFile
		if json.Unmarshal(data, &f) == nil && addEntries(f.MCPServers) {
			sources = append(sources, mcpJSONPath)
		}
	}

	if home != "" {
		claudeJSONPath := filepath.Join(home, ".claude.json")
		if data, err := os.ReadFile(claudeJSONPath); err == nil {
			var cc claudeUserConfig
			if json.Unmarshal(data, &cc) == nil {
				contributed := addEntries(cc.MCPServers)
				if proj, ok := cc.Projects[cwd]; ok {
					if addEntries(proj.MCPServers) {
						contributed = true
					}
				}
				if contributed {
					sources = append(sources, claudeJSONPath)
				}
			}
		}
	}

	return servers, sources, unusable
}

func mcpImportClaude(args []string) error {
	var scope string
	skipExisting := false
	for idx := 0; idx < len(args); idx++ {
		switch a := args[idx]; {
		case a == "-s" || a == "--scope":
			if idx+1 >= len(args) {
				return fmt.Errorf("--scope requires an argument (global|project)")
			}
			scope = args[idx+1]
			idx++
		case a == "--skip-existing":
			skipExisting = true
		default:
			return fmt.Errorf("unknown argument %q (usage: localcode mcp import-claude [-s global|project] [--skip-existing])", a)
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "" // still try ./.mcp.json even if the home dir can't be resolved
	}

	found, sources, unusable := collectClaudeMCPServers(cwd, home)
	if len(found) == 0 {
		fmt.Println("No importable Claude Code MCP servers found (checked ./.mcp.json and ~/.claude.json).")
		if len(unusable) > 0 {
			fmt.Printf("Skipped %d entry/entries that couldn't be read: %s\n", len(unusable), strings.Join(unusable, ", "))
		}
		return nil
	}

	path, err := resolveScopePath(scope)
	if err != nil {
		return err
	}
	existing, err := config.LoadFile(path)
	if err != nil {
		return err
	}

	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)

	// Re-running the import is the common case (a Claude Code setup
	// changed, or the first run only partially matched what's local), so
	// a name that's already registered gets overwritten with the freshly
	// imported definition by default, the same way `mcp add` does.
	// --skip-existing opts back into leaving those alone.
	added := []string{}
	overwritten := []string{}
	skippedExisting := []string{}
	if err := config.UpdateMCPServersInFile(path, func(servers map[string]config.MCPServerConfig) {
		for _, name := range names {
			if _, exists := existing.MCPServers[name]; exists {
				if skipExisting {
					skippedExisting = append(skippedExisting, name)
					continue
				}
				overwritten = append(overwritten, name)
			} else {
				added = append(added, name)
			}
			servers[name] = found[name]
		}
	}); err != nil {
		return err
	}

	fmt.Printf("Read from: %s\n", strings.Join(sources, ", "))
	if len(added) == 0 && len(overwritten) == 0 {
		fmt.Printf("Nothing new to import into %s (%s scope) — every server already exists (drop --skip-existing to overwrite them).\n", path, scopeLabel(scope))
		return nil
	}
	fmt.Printf("Imported into %s (%s scope):\n", path, scopeLabel(scope))
	for _, name := range added {
		fmt.Printf("  %s: %s\n", name, formatMCPCommand(found[name]))
	}
	for _, name := range overwritten {
		fmt.Printf("  %s: %s  (overwrote the existing entry)\n", name, formatMCPCommand(found[name]))
	}
	if len(skippedExisting) > 0 {
		fmt.Printf("Skipped %d already-registered server(s) (--skip-existing was set): %s\n", len(skippedExisting), strings.Join(skippedExisting, ", "))
	}
	if len(unusable) > 0 {
		fmt.Printf("Skipped %d entry/entries that couldn't be read: %s\n", len(unusable), strings.Join(unusable, ", "))
	}
	return nil
}

// formatMCPCommand renders a one-line summary of a server for listings.
// Header and env *values* are never printed, only their key names: a remote
// server's headers are where its API token lives, and `mcp list` output
// routinely ends up in a terminal scrollback or a pasted bug report.
func formatMCPCommand(sc config.MCPServerConfig) string {
	if sc.IsRemote() {
		s := fmt.Sprintf("%s %s", sc.Transport(), sc.URL)
		if keys := sortedKeys(sc.Headers); len(keys) > 0 {
			s += fmt.Sprintf(" (headers: %s)", strings.Join(keys, ", "))
		}
		return s
	}
	s := strings.Join(append([]string{sc.Command}, sc.Args...), " ")
	if keys := sortedKeys(sc.Env); len(keys) > 0 {
		s += fmt.Sprintf(" (env: %s)", strings.Join(keys, ", "))
	}
	return s
}

func sortedKeys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
