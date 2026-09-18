package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A SKILL.md written on Windows loads.
//
// A Windows editor ends lines with CRLF, and Notepad adds a byte-order
// mark. The frontmatter check compared the file against "---\n", so a
// file that plainly began with "---" was rejected as "missing YAML
// frontmatter" — every skill written on Windows, with the author told the
// file did not start with the line it started with. The bytes are fed in
// directly, so the Windows case is exercised on every platform.
func TestASkillWrittenOnWindowsLoads(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{"CRLF", "---\r\nname: jira-summary\r\ndescription: Track updates\r\n---\r\n\r\n# Purpose\r\nTrack updates.\r\n"},
		{"BOM+CRLF", "\xEF\xBB\xBF---\r\nname: jira-summary\r\ndescription: Track updates\r\n---\r\n\r\n# Purpose\r\nTrack updates.\r\n"},
		{"BOM+LF", "\xEF\xBB\xBF---\nname: jira-summary\ndescription: Track updates\n---\n\n# Purpose\nTrack updates.\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sk, err := ParseContent("SKILL.md", tc.content)
			if err != nil {
				t.Fatalf("a skill that begins with --- was rejected: %v", err)
			}
			if sk.Name != "jira-summary" || sk.Description != "Track updates" {
				t.Errorf("frontmatter not read: name=%q description=%q", sk.Name, sk.Description)
			}
			if strings.Contains(sk.Body, "\r") {
				t.Errorf("the body still carries carriage returns: %q", sk.Body)
			}
			if !strings.Contains(sk.Body, "# Purpose") {
				t.Errorf("the body is not the text after the frontmatter: %q", sk.Body)
			}
		})
	}
}

// And through the loader, from disk, the way the daemon reads it.
func TestLoadAllRegistersASkillSavedWithCRLF(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "jira-summary"), 0o755); err != nil {
		t.Fatal(err)
	}
	crlf := "\xEF\xBB\xBF---\r\nname: jira-summary\r\ndescription: Track updates\r\n---\r\n# Purpose\r\n"
	if err := os.WriteFile(filepath.Join(dir, "jira-summary", "SKILL.md"), []byte(crlf), 0o644); err != nil {
		t.Fatal(err)
	}
	list, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(list) != 1 || list[0].Name != "jira-summary" {
		t.Fatalf("a CRLF skill was not registered; got %+v", list)
	}
}
