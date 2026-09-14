package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, dir, name, frontmatter, body string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "---\n" + frontmatter + "\n---\n" + body
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
}

func TestLoadAll(t *testing.T) {
	global := t.TempDir()
	project := t.TempDir()

	writeSkill(t, global, "pdf", "name: pdf\ndescription: Work with PDF files", "# PDF skill\nDo the PDF thing.")
	writeSkill(t, global, "xlsx", "name: xlsx\ndescription: Work with spreadsheets", "# XLSX skill\nDo the XLSX thing.")
	// project overrides the "pdf" skill from global
	writeSkill(t, project, "pdf-override", "name: pdf\ndescription: PROJECT override", "project body")

	list, err := LoadAll(project, global)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 skills (pdf overridden + xlsx), got %d: %+v", len(list), list)
	}

	byName := map[string]Skill{}
	for _, s := range list {
		byName[s.Name] = s
	}

	pdf, ok := byName["pdf"]
	if !ok {
		t.Fatal("expected a \"pdf\" skill")
	}
	if pdf.Description != "PROJECT override" {
		t.Errorf("expected project-local pdf skill to win, got description %q", pdf.Description)
	}
	if pdf.Body != "project body" {
		t.Errorf("body = %q, want %q", pdf.Body, "project body")
	}

	if _, ok := byName["xlsx"]; !ok {
		t.Error("expected an \"xlsx\" skill from the global dir")
	}
}

func TestLoadAllSkipsMalformed(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "broken")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// No frontmatter at all.
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("just some text"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	writeSkill(t, dir, "good", "name: good\ndescription: fine", "body")

	list, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(list) != 1 || list[0].Name != "good" {
		t.Errorf("expected only the well-formed skill to load, got %+v", list)
	}
}

func TestASymlinkedSkillDirectoryIsLoaded(t *testing.T) {
	dir := t.TempDir()
	target := t.TempDir()

	writeSkill(t, target, "linked", "name: linked\ndescription: via a symlink", "linked body")
	if err := os.Symlink(filepath.Join(target, "linked"), filepath.Join(dir, "linked")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	list, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(list) != 1 || list[0].Name != "linked" || list[0].Body != "linked body" {
		t.Errorf("symlinked skill directory was not loaded, got %+v", list)
	}
}

func TestABrokenSymlinkIsSkippedLikeAMalformedSkill(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "broken")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	writeSkill(t, dir, "good", "name: good\ndescription: fine", "body")

	list, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("a broken symlink must not fail startup, got: %v", err)
	}
	if len(list) != 1 || list[0].Name != "good" {
		t.Errorf("expected only the well-formed skill to load, got %+v", list)
	}
}

func TestASymlinkToAFileIsSkippedLikeAMalformedSkill(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "plain.md")
	if err := os.WriteFile(file, []byte("not a skill dir"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(file, filepath.Join(dir, "filelink")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	writeSkill(t, dir, "good", "name: good\ndescription: fine", "body")

	list, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("a symlink to a file must not fail startup, got: %v", err)
	}
	if len(list) != 1 || list[0].Name != "good" {
		t.Errorf("expected only the well-formed skill to load, got %+v", list)
	}
}

func TestParseContentReadsAFileOfAnyName(t *testing.T) {
	sk, err := ParseContent("/tmp/scratch.md",
		"---\nname: scratch\ndescription: pointed at directly\n---\nDo the thing.")
	if err != nil {
		t.Fatalf("ParseContent: %v", err)
	}
	if sk.Body != "Do the thing." {
		t.Errorf("body = %q, want %q", sk.Body, "Do the thing.")
	}
}

func TestSystemPromptSection(t *testing.T) {
	if got := SystemPromptSection(nil); got != "" {
		t.Errorf("empty list should render empty string, got %q", got)
	}

	list := []Skill{{Name: "pdf", Description: "Work with PDF files"}}
	got := SystemPromptSection(list)
	if got == "" {
		t.Fatal("expected non-empty section for a non-empty skill list")
	}
	if !strings.Contains(got, "pdf") || !strings.Contains(got, "Work with PDF files") {
		t.Errorf("section missing expected content: %q", got)
	}
}
