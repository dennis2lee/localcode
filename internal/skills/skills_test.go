package skills

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"runtime"
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

// Running localcode in a home directory makes the project root and the
// person's root one directory, and both are handed to the loader. A skill
// that cannot be read was then blamed twice for the same thing: seven
// unreadable skills produced fourteen lines, which reads as fourteen
// problems.
func TestOneDirectoryGivenTwiceIsReadOnce(t *testing.T) {
	root := t.TempDir()
	skillsDir := filepath.Join(root, "skills")
	write := func(name, body string) {
		t.Helper()
		dir := filepath.Join(skillsDir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("good", "---\nname: good\ndescription: fine\n---\nbody\n")
	write("bad", "# no frontmatter at all\n")

	list, warnings, err := LoadAllWithWarnings(skillsDir, skillsDir)
	if err != nil {
		t.Fatalf("LoadAllWithWarnings: %v", err)
	}
	if len(warnings) != 1 {
		t.Errorf("one unreadable skill produced %d warnings: %v", len(warnings), warnings)
	}
	if len(list) != 1 || list[0].Name != "good" {
		t.Errorf("loaded %d skill(s), want the one that reads", len(list))
	}
}

// Every root is read now, so a root that used to lose is read too, and what
// is in it is anybody's guess: a leftover file where "skills" should be, a
// directory somebody made unreadable, a link that goes round in a circle.
// Each of those costs that directory its skills and nothing else, with one
// line saying which directory and why. None of them stops the load.
func TestADirectoryThatCannotBeListedIsOneWarningAndNothingElse(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "good")
	writeSkill(t, good, "fine", "name: fine\ndescription: still loads", "body")

	file := filepath.Join(root, "a-file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	list, warnings, err := LoadAllWithWarnings(file, good)
	if err != nil {
		t.Fatalf("a file where a skills directory should be stopped the load: %v", err)
	}
	if len(list) != 1 || list[0].Name != "fine" {
		t.Errorf("loaded %+v, want the skill from the directory that can be read", list)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], file) || !strings.Contains(warnings[0], "cannot be read") {
		t.Errorf("warnings = %v, want one naming %s", warnings, file)
	}
	if strings.Count(warnings[0], file) != 1 {
		t.Errorf("the path is said twice in %q", warnings[0])
	}
}

func TestAnUnreadableSkillsDirectoryIsOneWarning(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("a directory mode cannot be made to refuse here")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	writeSkill(t, locked, "hidden", "name: hidden\ndescription: behind a mode", "body")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	good := filepath.Join(root, "good")
	writeSkill(t, good, "fine", "name: fine\ndescription: still loads", "body")

	list, warnings, err := LoadAllWithWarnings(locked, good)
	if err != nil {
		t.Fatalf("a directory that cannot be read stopped the load: %v", err)
	}
	if len(list) != 1 || list[0].Name != "fine" || len(warnings) != 1 {
		t.Errorf("loaded %+v with warnings %v", list, warnings)
	}
}

func TestALinkThatGoesRoundIsOneWarning(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	if err := os.Symlink(b, a); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	good := filepath.Join(root, "good")
	writeSkill(t, good, "fine", "name: fine\ndescription: still loads", "body")

	list, warnings, err := LoadAllWithWarnings(a, good)
	if err != nil {
		t.Fatalf("a link that loops stopped the load: %v", err)
	}
	if len(list) != 1 || len(warnings) != 1 {
		t.Errorf("loaded %+v with warnings %v", list, warnings)
	}
}

// Every root is read now, and the ordinary way to share one set of skills
// between two agents is to link one root's directory to the other's. The
// same files under two names were read twice and reported twice, so the
// directory is judged by where it is, not by what it is called.
func TestADirectoryReachedByALinkIsReadOnce(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "claude", "skills")
	linked := filepath.Join(root, "opencode", "skills")
	writeSkill(t, real, "good", "name: good\ndescription: fine", "body")
	if err := os.MkdirAll(filepath.Join(real, "bad"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "bad", "SKILL.md"), []byte("# no frontmatter at all\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(linked), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, linked); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	list, warnings, err := LoadAllWithWarnings(real, linked)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 {
		t.Errorf("one unreadable skill, reached under two names, produced %d warnings: %v", len(warnings), warnings)
	}
	if len(list) != 1 || list[0].Name != "good" {
		t.Errorf("loaded %+v, want the one skill that reads, once", list)
	}
}

// And a Windows editor's file reads, which is the other half of the same
// complaint: a SKILL.md written in Notepad begins with three bytes nobody
// can see and ends its lines with CRLF.
func TestASkillWrittenInNotepadReads(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "skills", "del-comment")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bom := string([]byte{0xEF, 0xBB, 0xBF})
	body := bom + "---\r\nname: del-comment\r\ndescription: Delete What-Comments\r\n---\r\n# Comment delete skill\r\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	list, warnings, err := LoadAllWithWarnings(filepath.Join(root, "skills"))
	if err != nil {
		t.Fatalf("LoadAllWithWarnings: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("a Notepad-written skill was refused: %v", warnings)
	}
	if len(list) != 1 || list[0].Name != "del-comment" || list[0].Description != "Delete What-Comments" {
		t.Errorf("loaded %+v, want the skill with its name and description", list)
	}
}
