package memory

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"/Users/dennis/work/localcode": "Users-dennis-work-localcode",
		"C:\\code\\proj":               "C-code-proj",
		"already-clean":                "already-clean",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDirFallsBackToProjectDirOutsideGitRepo(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir() // not a git repo

	dir := Dir(project, home)
	want := filepath.Join(home, ".localcode", "projects", slugify(project), "memory")
	if dir != want {
		t.Errorf("Dir() = %q, want %q", dir, want)
	}
}

func TestDirUsesGitRootWhenInsideRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	dirFromRoot := Dir(root, home)
	dirFromSub := Dir(sub, home)
	if dirFromRoot != dirFromSub {
		t.Errorf("Dir(root)=%q and Dir(subdir)=%q should match (same repo shares one memory dir)", dirFromRoot, dirFromSub)
	}
}

func TestLoadIndexMissingReturnsEmpty(t *testing.T) {
	if got := LoadIndex(t.TempDir()); got != "" {
		t.Errorf("LoadIndex() = %q, want empty for a nonexistent MEMORY.md", got)
	}
}

func TestLoadIndexReadsContent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(IndexPath(dir), []byte("- fact one\n- fact two"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LoadIndex(dir)
	if !strings.Contains(got, "fact one") || !strings.Contains(got, "fact two") {
		t.Errorf("LoadIndex() = %q, want both entries", got)
	}
}

func TestLoadIndexCapsAtMaxLines(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 0; i < maxIndexLines+50; i++ {
		b.WriteString("line\n")
	}
	if err := os.WriteFile(IndexPath(dir), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LoadIndex(dir)
	lines := strings.Split(got, "\n")
	if len(lines) > maxIndexLines {
		t.Errorf("LoadIndex() returned %d lines, want capped at %d", len(lines), maxIndexLines)
	}
}

func TestLoadIndexCapsAtMaxBytes(t *testing.T) {
	dir := t.TempDir()
	huge := strings.Repeat("x", maxIndexBytes*2)
	if err := os.WriteFile(IndexPath(dir), []byte(huge), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LoadIndex(dir)
	if len(got) > maxIndexBytes {
		t.Errorf("LoadIndex() returned %d bytes, want capped at %d", len(got), maxIndexBytes)
	}
}

func TestSystemPromptSectionMentionsDirAndIndex(t *testing.T) {
	section := SystemPromptSection("/tmp/mem", "- some fact")
	if !strings.Contains(section, "/tmp/mem") {
		t.Error("expected the memory directory path in the section")
	}
	if !strings.Contains(section, "some fact") {
		t.Error("expected the current index content in the section")
	}
}

func TestSystemPromptSectionEmptyIndex(t *testing.T) {
	section := SystemPromptSection("/tmp/mem", "")
	if !strings.Contains(section, "No memory index exists yet") {
		t.Errorf("SystemPromptSection() = %q, want a note that no index exists yet", section)
	}
}
