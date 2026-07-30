package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"localcode/internal/config"
)

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
