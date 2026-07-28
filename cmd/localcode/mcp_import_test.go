package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCollectClaudeMCPServers confirms the three Claude Code sources (a
// shared ./.mcp.json, ~/.claude.json's global mcpServers, and its
// per-project block) all contribute, that a remote/url-based entry is
// counted as skipped rather than imported, and that a later source
// (the project-scoped Claude entry) wins a name collision.
func TestCollectClaudeMCPServers(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()

	mcpJSON := `{"mcpServers":{"github":{"command":"npx","args":["-y","server-github"],"env":{"TOKEN":"x"}}}}`
	if err := os.WriteFile(filepath.Join(cwd, ".mcp.json"), []byte(mcpJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	claudeJSON := `{
		"mcpServers": {
			"filesystem": {"command": "npx", "args": ["-y", "server-filesystem"]},
			"remote": {"url": "https://example.com/mcp"},
			"github": {"command": "old-github-command"}
		},
		"projects": {
			"` + escapeJSONPath(cwd) + `": {
				"mcpServers": {
					"github": {"command": "new-github-command"}
				}
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(claudeJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	servers, sources, skippedRemote := collectClaudeMCPServers(cwd, home)

	if len(sources) != 2 {
		t.Errorf("sources = %v, want 2 contributing files", sources)
	}
	if skippedRemote != 1 {
		t.Errorf("skippedRemote = %d, want 1 (the url-based \"remote\" entry)", skippedRemote)
	}
	if _, ok := servers["remote"]; ok {
		t.Error("url-based server should not appear in the imported set")
	}
	if got := servers["filesystem"].Command; got != "npx" {
		t.Errorf("filesystem.command = %q, want npx", got)
	}
	// The project-scoped Claude entry (read last) should win over both the
	// project's own ./.mcp.json entry and Claude's global entry.
	if got := servers["github"].Command; got != "new-github-command" {
		t.Errorf("github.command = %q, want the project-scoped override %q", got, "new-github-command")
	}
}

func TestCollectClaudeMCPServersNoSources(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()

	servers, sources, skippedRemote := collectClaudeMCPServers(cwd, home)
	if len(servers) != 0 || len(sources) != 0 || skippedRemote != 0 {
		t.Errorf("expected nothing found, got servers=%v sources=%v skippedRemote=%d", servers, sources, skippedRemote)
	}
}

func escapeJSONPath(p string) string {
	out := make([]byte, 0, len(p))
	for _, r := range p {
		if r == '\\' {
			out = append(out, '\\', '\\')
			continue
		}
		out = append(out, byte(r))
	}
	return string(out)
}
