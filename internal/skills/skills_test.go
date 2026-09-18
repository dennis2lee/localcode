package skills

import (
	"bytes"
	"log"
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

// A skill directory holding a SKILL.md that will not parse is an attempt
// at a skill, and skipping it must say so: one warning naming the path
// and the reason. The good skill beside it still loads, and the load
// still reports no error — a malformed skill must not stop startup.
func TestAMalformedSkillProducesAWarningNamingPathAndReason(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		// No frontmatter at all.
		"plain": "just some text",
		// A blank first line: the cruel case, invisible in an editor.
		"blank-first-line": "\n---\nname: blank\ndescription: blank\n---\nbody\n",
		// Not here: a byte-order mark before the dashes. It was a malformed
		// case until v0.134.0, when it turned out to be what every Windows
		// editor writes; NormalizeMarkdown strips it and the skill loads.
		// See TestASkillWrittenOnWindowsLoads.
		// An unterminated frontmatter block.
		"unterminated": "---\nname: unterminated\ndescription: no closing dashes\n",
		// Frontmatter that is not YAML.
		"bad-yaml": "---\nname: [unclosed\ndescription: bad\n---\nbody\n",
	}
	for name, content := range cases {
		skillDir := filepath.Join(dir, name)
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	writeSkill(t, dir, "good", "name: good\ndescription: fine", "body")

	list, warnings, err := LoadAllWithWarnings(dir)
	if err != nil {
		t.Fatalf("a malformed skill must not fail the load: %v", err)
	}
	if len(list) != 1 || list[0].Name != "good" {
		t.Errorf("expected only the well-formed skill to load, got %+v", list)
	}
	if len(warnings) != len(cases) {
		t.Fatalf("warnings = %q, want one per malformed skill", warnings)
	}
	for name := range cases {
		path := filepath.Join(dir, name, "SKILL.md")
		var saw bool
		for _, w := range warnings {
			if strings.Contains(w, path) {
				saw = true
				if !strings.Contains(w, "frontmatter") && !strings.Contains(w, "YAML") && !strings.Contains(w, "yaml") {
					t.Errorf("warning %q names %q but says nothing about what is wrong with it", w, path)
				}
			}
		}
		if !saw {
			t.Errorf("no warning names %q (warnings: %q)", path, warnings)
		}
	}
}

// A directory with no SKILL.md is not an attempt at a skill — it may be
// notes or resources living beside the skills — and neither is a plain
// file, so both stay silent while the real skill beside them loads.
func TestADirectoryWithoutASkillFileStaysSilent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "notes"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes", "todo.md"), []byte("not a skill"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stray.md"), []byte("not a skill dir"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	writeSkill(t, dir, "good", "name: good\ndescription: fine", "body")

	list, warnings, err := LoadAllWithWarnings(dir)
	if err != nil {
		t.Fatalf("LoadAllWithWarnings: %v", err)
	}
	if len(list) != 1 || list[0].Name != "good" {
		t.Errorf("expected only the well-formed skill to load, got %+v", list)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %q, want silence for entries that are not skill attempts", warnings)
	}
}

// A name that loses to an earlier directory loaded fine from the winner,
// so it is not a skip worth mentioning.
func TestAnOverriddenSkillStaysSilent(t *testing.T) {
	project := t.TempDir()
	global := t.TempDir()
	writeSkill(t, project, "proj", "name: dup\ndescription: project copy", "project")
	writeSkill(t, global, "glob", "name: dup\ndescription: global copy", "global")

	list, warnings, err := LoadAllWithWarnings(project, global)
	if err != nil {
		t.Fatalf("LoadAllWithWarnings: %v", err)
	}
	if len(list) != 1 || list[0].Description != "project copy" {
		t.Errorf("expected the project copy to win silently, got %+v", list)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %q, want silence for an override", warnings)
	}
}

// LoadAll surfaces the same warnings as one log line each: that line is
// what a person debugging "where did my skill go" actually sees.
func TestLoadAllLogsOneLinePerSkippedSkill(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	dir := t.TempDir()
	skillDir := filepath.Join(dir, "broken")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
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
	out := buf.String()
	if !strings.Contains(out, "skills: skipping") {
		t.Errorf("log = %q, want one line saying the skill was skipped", out)
	}
	if !strings.Contains(out, filepath.Join(skillDir, "SKILL.md")) {
		t.Errorf("log = %q, want the skipped path named", out)
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
