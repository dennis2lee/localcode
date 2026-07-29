package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCollectClaudeMCPServers confirms the three Claude Code sources (a
// shared ./.mcp.json, ~/.claude.json's global mcpServers, and its
// per-project block) all contribute, that remote entries import alongside
// local ones, and that a later source (the project-scoped Claude entry)
// wins a name collision.
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
			"bare-url": {"url": "https://example.com/mcp"},
			"remote-http": {"type": "http", "url": "https://example.com/mcp", "headers": {"Authorization": "Bearer t"}},
			"remote-sse": {"type": "sse", "url": "https://example.com/sse"},
			"broken": {"env": {"NOTHING": "useful"}},
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

	servers, sources, unusable := collectClaudeMCPServers(cwd, home)

	if len(sources) != 2 {
		t.Errorf("sources = %v, want 2 contributing files", sources)
	}
	if got := servers["filesystem"].Command; got != "npx" {
		t.Errorf("filesystem.command = %q, want npx", got)
	}

	// Remote servers import too, keeping their transport, url, and headers.
	if got := servers["remote-http"]; got.Transport() != "http" || got.URL != "https://example.com/mcp" {
		t.Errorf("remote-http = %+v, want an http server at https://example.com/mcp", got)
	}
	if got := servers["remote-http"].Headers["Authorization"]; got != "Bearer t" {
		t.Errorf("remote-http Authorization header = %q, want it carried across", got)
	}
	if got := servers["remote-sse"].Transport(); got != "sse" {
		t.Errorf("remote-sse transport = %q, want sse — an explicit type must not be rewritten", got)
	}
	// A url with no type is a remote server; http is the current spec's
	// transport, so that is what a bare url infers.
	if got := servers["bare-url"].Transport(); got != "http" {
		t.Errorf("bare-url transport = %q, want http", got)
	}

	// An entry that says neither how to start a process nor where to
	// connect is reported by name rather than silently dropped.
	if _, ok := servers["broken"]; ok {
		t.Error("an entry with neither command nor url should not be imported")
	}
	if len(unusable) != 1 || !strings.Contains(unusable[0], "broken") {
		t.Errorf("unusable = %v, want the one unreadable \"broken\" entry", unusable)
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

	servers, sources, unusable := collectClaudeMCPServers(cwd, home)
	if len(servers) != 0 || len(sources) != 0 || len(unusable) != 0 {
		t.Errorf("expected nothing found, got servers=%v sources=%v unusable=%v", servers, sources, unusable)
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
