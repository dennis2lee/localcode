package memory

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
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

// TestLoadIndexByteCapKeepsValidUTF8 guards against the byte cap slicing
// through a multi-byte rune (CJK/emoji), which would emit invalid UTF-8.
func TestLoadIndexByteCapKeepsValidUTF8(t *testing.T) {
	dir := t.TempDir()
	// "가" is 3 bytes in UTF-8; maxIndexBytes is not a multiple of 3, so the
	// cap lands mid-rune.
	big := strings.Repeat("가", maxIndexBytes) // ~3x over the byte cap
	if err := os.WriteFile(IndexPath(dir), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	got := LoadIndex(dir)
	if !utf8.ValidString(got) {
		t.Errorf("LoadIndex() produced invalid UTF-8 at the byte boundary")
	}
	if len(got) > maxIndexBytes {
		t.Errorf("LoadIndex() = %d bytes, want <= %d", len(got), maxIndexBytes)
	}
}

// PolicySection and IndexSection are tested directly rather than through
// a combining wrapper. They used to be one function, SystemPromptSection,
// concatenating the two into a single system-prompt block; the v0.57.0
// trust split gave them separate call sites instead (wire.go passes each
// to its own prompt asset — see internal/agent/prompt_assets.go), because
// folding the model's own recalled notes into the same block as product
// instruction let a previous turn's text inherit the policy's authority.
// Testing the wrapper after that split was testing a function nothing
// built the real prompt with; these two are what wire.go actually calls.
func TestPolicySectionMentionsDirAndIndexPath(t *testing.T) {
	section := PolicySection("/tmp/mem")
	if !strings.Contains(section, "/tmp/mem") {
		t.Error("expected the memory directory path in the section")
	}
	if !strings.Contains(section, IndexPath("/tmp/mem")) {
		t.Error("expected the index file's path in the section")
	}
}

func TestIndexSectionWrapsTheRecalledNotes(t *testing.T) {
	section := IndexSection("- some fact")
	if !strings.Contains(section, "some fact") {
		t.Error("expected the current index content in the section")
	}
	if !strings.Contains(section, "not instructions") {
		t.Error("expected the boundary explaining these are a record, not instructions")
	}
}

// Empty index, empty section — the doc comment's claim, and the reason
// the asset that renders this is skipped entirely rather than emitting a
// placeholder (see internal/agent/prompt_assets.go): there is nothing to
// tell the model about "no index yet" that PolicySection has not already
// said by describing the convention.
func TestIndexSectionEmptyIndexIsEmptySection(t *testing.T) {
	if section := IndexSection(""); section != "" {
		t.Errorf("IndexSection(\"\") = %q, want empty", section)
	}
}
