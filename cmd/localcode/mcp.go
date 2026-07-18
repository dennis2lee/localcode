// `localcode mcp` manages MCP server entries in config.json from the
// command line, Claude Code-style (`claude mcp add/list/get/remove`) —
// so a server can be registered without hand-editing JSON. It only edits
// the config file(s); a running daemon picks up changes on its next
// start (or reconnect), same as any other config.json edit.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"localcode/internal/config"
)

const mcpUsage = `사용법: localcode mcp <subcommand>

  localcode mcp add [-e KEY=VALUE]... [-s global|project] <name> -- <command> [args...]
                       새 MCP 서버 등록 (stdio 실행, .mcp.json의 mcpServers 항목과 같은 모양)
  localcode mcp add-json [-s global|project] <name> '<json>'
                       {"command":...,"args":[...],"env":{...}} 형태의 JSON으로 직접 등록
  localcode mcp list    등록된 MCP 서버 전체 목록 (global/project 출처 표시)
  localcode mcp get <name>       서버 하나의 상세 설정 확인
  localcode mcp remove [-s global|project] <name>
                       서버 제거 (scope 생략 시 project 우선, 양쪽에 다 있으면 --scope 필요)

  -s, --scope   global (기본값, ~/.localcode/config.json) 또는 project (./.localcode/config.json)

편집한 내용은 다음 데몬 시작(또는 재연결) 시점부터 반영됩니다.`

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
		return mcpList()
	case "get":
		return mcpGet(args[1:])
	case "remove", "rm":
		return mcpRemove(args[1:])
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

func mcpAdd(args []string) error {
	var name, scope string
	env := map[string]string{}

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
		return fmt.Errorf("usage: localcode mcp add [-e KEY=VALUE]... [-s global|project] <name> -- <command> [args...]")
	}
	if len(rest) == 0 {
		return fmt.Errorf("missing command to run the MCP server, e.g.: localcode mcp add %s -- npx -y @modelcontextprotocol/server-github", name)
	}
	command, cmdArgs := rest[0], rest[1:]

	path, err := resolveScopePath(scope)
	if err != nil {
		return err
	}
	sc := config.MCPServerConfig{Command: command, Args: cmdArgs, Env: env}
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
	if sc.Command == "" {
		return fmt.Errorf(`server json must include a "command" field`)
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

func mcpList() error {
	global, project, globalPath, projectPath, err := loadBothScopes()
	if err != nil {
		return err
	}
	if len(global.MCPServers) == 0 && len(project.MCPServers) == 0 {
		fmt.Println("등록된 MCP 서버가 없습니다. `localcode mcp add`로 추가하세요.")
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

	for _, n := range sorted {
		if sc, ok := project.MCPServers[n]; ok {
			note := ""
			if _, alsoGlobal := global.MCPServers[n]; alsoGlobal {
				note = " (global 설정을 덮어씀)"
			}
			fmt.Printf("%s  [project]%s\n  %s\n  %s\n", n, note, formatMCPCommand(sc), projectPath)
			continue
		}
		sc := global.MCPServers[n]
		fmt.Printf("%s  [global]\n  %s\n  %s\n", n, formatMCPCommand(sc), globalPath)
	}
	return nil
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
			fmt.Println("  (global 설정도 있지만 project 설정이 우선 적용됩니다)")
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
	fmt.Printf("  command: %s\n", sc.Command)
	if len(sc.Args) > 0 {
		fmt.Printf("  args:    %s\n", strings.Join(sc.Args, " "))
	}
	if len(sc.Env) > 0 {
		keys := make([]string, 0, len(sc.Env))
		for k := range sc.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("  env:     %s=%s\n", k, sc.Env[k])
		}
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

func formatMCPCommand(sc config.MCPServerConfig) string {
	parts := append([]string{sc.Command}, sc.Args...)
	s := strings.Join(parts, " ")
	if len(sc.Env) > 0 {
		keys := make([]string, 0, len(sc.Env))
		for k := range sc.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		s += fmt.Sprintf(" (env: %s)", strings.Join(keys, ", "))
	}
	return s
}
