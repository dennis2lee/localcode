package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A custom command written on Windows keeps its frontmatter.
//
// The subtler half of the CRLF defect: a command with no frontmatter is
// not an error, the whole file is the body. So a CRLF command file did
// not fail — it lost its agent: pin and description, and sent its YAML
// lines to the model as the first lines of the prompt. The bytes are fed
// in directly, so the Windows case runs everywhere.
func TestACommandWrittenOnWindowsKeepsItsFrontmatter(t *testing.T) {
	dir := t.TempDir()
	crlf := "\xEF\xBB\xBF---\r\ndescription: Summarise the sprint\r\nagent: reviewer\r\n---\r\nSummarise $ARGUMENTS.\r\n"
	path := filepath.Join(dir, "sprint.md")
	if err := os.WriteFile(path, []byte(crlf), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd, err := parseCommandFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cmd.Description != "Summarise the sprint" {
		t.Errorf("description lost: %q", cmd.Description)
	}
	if cmd.Agent != "reviewer" {
		t.Errorf("agent: pin lost: %q — the command would run as the session's agent, not the one it names", cmd.Agent)
	}
	if strings.Contains(cmd.Body, "agent:") || strings.Contains(cmd.Body, "---") {
		t.Errorf("the YAML reached the body, so it would be sent to the model: %q", cmd.Body)
	}
	if strings.Contains(cmd.Body, "\r") {
		t.Errorf("the body still carries carriage returns: %q", cmd.Body)
	}
}
