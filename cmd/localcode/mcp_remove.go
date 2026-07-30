package main

import (
	"fmt"

	"localcode/internal/config"
)

func mcpRemove(args []string) error {
	scope, positional, err := parseScope(args)
	if err != nil {
		return err
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

// removeMCPServerFromFile aborts with "not found" from inside the update
// callback rather than checking existence with a separate read first, so a
// not-found name still doesn't rewrite (reformat) the file — the mutate
// error leaves it untouched — without needing to parse it twice.
func removeMCPServerFromFile(path, name string) error {
	return config.UpdateMCPServersInFile(path, func(servers map[string]config.MCPServerConfig) error {
		if _, ok := servers[name]; !ok {
			return fmt.Errorf("mcp server %q not found in %s", name, path)
		}
		delete(servers, name)
		return nil
	})
}
