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
	if err := config.UpdateMCPServersInFile(path, func(servers map[string]config.MCPServerConfig) error {
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
		return nil
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
