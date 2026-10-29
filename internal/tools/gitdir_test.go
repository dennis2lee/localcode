package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGitDirNames(t *testing.T) {
	for _, name := range []string{
		".git", ".GIT", ".Git", ".g\u200cit", "\ufeff.git", ".gi\u202et",
		".git.", ".git ", ".git. .", ".git::$INDEX_ALLOCATION", ".git:stream",
		"GIT~1", "git~12",
	} {
		if !isGitDirName(name) {
			t.Errorf("isGitDirName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{
		"", "git", ".github", ".gitignore", ".gitattributes", ".gitmodules",
		"x.git", "git~", "git~1a", ".git2", "src", "..", ".",
	} {
		if isGitDirName(name) {
			t.Errorf("isGitDirName(%q) = true, want false", name)
		}
	}
}

func TestIntoGitDir(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkingDir(context.Background(), workspace)

	for _, path := range []string{
		".git/config", "./.git/config", ".git/hooks/pre-commit", ".git",
		"src/../.git/config", "sub/.git/config", ".git/new/dir/file",
		filepath.Join(workspace, ".git", "config"),
		filepath.Join(filepath.Dir(workspace), "elsewhere", ".git", "config"),
	} {
		if !IntoGitDir(ctx, path) {
			t.Errorf("IntoGitDir(%q) = false, want true", path)
		}
	}
	for _, path := range []string{
		"", "src/main.go", ".github/workflows/ci.yml", ".gitignore",
		".gitmodules", "docs/git/notes.md", "x.git.md",
	} {
		if IntoGitDir(ctx, path) {
			t.Errorf("IntoGitDir(%q) = true, want false", path)
		}
	}
}

// A link is judged by where it leads: written through, it writes there.
func TestIntoGitDirThroughALink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	workspace := t.TempDir()
	git := filepath.Join(workspace, ".git")
	if err := os.MkdirAll(filepath.Join(git, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{
		"repo":    git,                                   // a link to .git itself
		"hooks":   filepath.Join(git, "hooks"),           // to a directory inside it
		"pending": filepath.Join(git, "hooks", "commit"), // dangling: the write creates the target
	} {
		if err := os.Symlink(target, filepath.Join(workspace, link)); err != nil {
			t.Fatal(err)
		}
	}
	ctx := WithWorkingDir(context.Background(), workspace)
	for _, path := range []string{"repo/config", "hooks/pre-commit", "pending"} {
		if !IntoGitDir(ctx, path) {
			t.Errorf("IntoGitDir(%q) = false, want true: the link leads into .git", path)
		}
	}

	// A path that cannot be resolved at all is still judged as written.
	if err := os.Symlink("loop-b", filepath.Join(workspace, "loop-a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("loop-a", filepath.Join(workspace, "loop-b")); err != nil {
		t.Fatal(err)
	}
	if !IntoGitDir(ctx, "loop-a/.git/config") {
		t.Error("IntoGitDir(\"loop-a/.git/config\") = false: a path that does not resolve still names .git")
	}
	// One that does not name .git is not counted: the boundary asks about
	// it as outside, and counting it here made every write through a
	// Windows junction a question no answer could cover.
	if IntoGitDir(ctx, "loop-a/notes.md") {
		t.Error("IntoGitDir(\"loop-a/notes.md\") = true: an unresolvable path is judged by its text")
	}
	if !OutsideWorkspace(ctx, "loop-a/notes.md") {
		t.Error("OutsideWorkspace(\"loop-a/notes.md\") = false: the boundary is what asks about it")
	}
}

// A link whose target ends in a separator names a directory, not an
// element called "": "link/file.txt" with link -> newdir/ is newdir/file.txt.
func TestALinkToADirectoryWithATrailingSeparator(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	workspace := t.TempDir()
	if err := os.Symlink("newdir/", filepath.Join(workspace, "link")); err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkingDir(context.Background(), workspace)
	if IntoGitDir(ctx, "link/file.txt") || OutsideWorkspace(ctx, "link/file.txt") {
		t.Errorf("link/file.txt: IntoGitDir=%v OutsideWorkspace=%v; it is newdir/file.txt in the workspace",
			IntoGitDir(ctx, "link/file.txt"), OutsideWorkspace(ctx, "link/file.txt"))
	}
}

// A glob is judged by the pattern it runs, which resolve cleans: "*/.."
// is gone before anything is listed.
func TestAGlobIsJudgedByThePatternItRuns(t *testing.T) {
	workspace := t.TempDir()
	root := filepath.Dir(workspace)
	for pattern, want := range map[string]string{
		workspace + "/*/../../secret/*":  filepath.Join(root, "secret"),
		workspace + "/**/../../secret/*": filepath.Join(root, "secret"),
		"*/../../secret/*":               filepath.Join("..", "secret"),
		"src/**/*.go":                    "src",
		"*.go":                           ".",
		"":                               "",
	} {
		input, _ := json.Marshal(map[string]string{"pattern": pattern})
		if got := (Glob{}).Subject(input); got != want {
			t.Errorf("Glob subject of %q = %q, want %q", pattern, got, want)
		}
	}
}

// The file system follows a link before it applies "..", so "h/.."
// with h a link to .git/hooks is .git. A dangling link whose target
// reads that way is judged where it leads.
func TestALinkTargetIsReadThroughItsLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	workspace := t.TempDir()
	git := filepath.Join(workspace, ".git")
	if err := os.MkdirAll(filepath.Join(git, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(git, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Spelled out, not built with filepath.Join, which would clean the
	// ".." away before the link is made.
	links := map[string]string{
		"h":         ".git/hooks",
		"notes.txt": "h/../info/attributes",
		"VERSION":   workspace + "/h/../commondir",
		"deep":      "h/../newdir/x",
	}
	for link, target := range links {
		if err := os.Symlink(target, filepath.Join(workspace, link)); err != nil {
			t.Fatal(err)
		}
	}
	ctx := WithWorkingDir(context.Background(), workspace)
	real, err := filepath.EvalSymlinks(git)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"notes.txt": filepath.Join(real, "info", "attributes"),
		"VERSION":   filepath.Join(real, "commondir"),
		"deep":      filepath.Join(real, "newdir", "x"),
	} {
		got, err := resolvePhysical(filepath.Join(workspace, path))
		if err != nil || got != want {
			t.Errorf("resolvePhysical(%s) = %q, %v; want %q", path, got, err, want)
		}
		if !IntoGitDir(ctx, path) {
			t.Errorf("IntoGitDir(%q) = false: it leads into .git", path)
		}
	}
}

// What the tools write is the path the checks judged. An absolute path
// with ".." after a link used to go to the file system uncleaned, and
// the file system followed the link first: "<ws>/h/../config" was judged
// as <ws>/config and written to .git/config.
func TestAToolWritesThePathItWasJudgedBy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	workspace := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(workspace, ".git", "hooks"), filepath.Join(workspace, "h")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(workspace, "out")); err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkingDir(context.Background(), workspace)

	for _, tc := range []struct{ path, lands, notIn string }{
		// Spelled out: filepath.Join would clean the ".." away first.
		{workspace + "/h/../config", filepath.Join(workspace, "config"), filepath.Join(workspace, ".git", "config")},
		{workspace + "/out/../escape.txt", filepath.Join(workspace, "escape.txt"), filepath.Join(filepath.Dir(elsewhere), "escape.txt")},
	} {
		if IntoGitDir(ctx, tc.path) || OutsideWorkspace(ctx, tc.path) {
			t.Errorf("%s: judged as into .git or outside; the write below shows where it goes", tc.path)
		}
		input, _ := json.Marshal(map[string]string{"path": tc.path, "content": "x\n"})
		if res := (WriteFile{}).Execute(ctx, input); res.IsError {
			t.Fatalf("write %s: %s", tc.path, res.Content)
		}
		if _, err := os.Stat(tc.lands); err != nil {
			t.Errorf("write %s did not land at %s, the path it was judged by: %v", tc.path, tc.lands, err)
		}
		if _, err := os.Stat(tc.notIn); err == nil {
			t.Errorf("write %s landed at %s, through the link", tc.path, tc.notIn)
		}
	}
}

func TestSplitLastElement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX spellings")
	}
	for path, want := range map[string][2]string{
		"/":         {"/", ""},
		"/a":        {"/", "a"},
		"/a/b":      {"/a", "b"},
		"/a/h/../x": {"/a/h/..", "x"},
		"a":         {".", "a"},
		"a/b":       {"a", "b"},
		"/a/b/":     {"/a", "b"},
		"/a/b//":    {"/a", "b"},
		"newdir/":   {".", "newdir"},
	} {
		parent, base := splitLastElement(path)
		if parent != want[0] || base != want[1] {
			t.Errorf("splitLastElement(%q) = %q, %q; want %q, %q", path, parent, base, want[0], want[1])
		}
	}
}

// A write into .git is a question asked every time, and says so, so no
// remembered answer covers it. Nothing else is.
func TestAGitDirQuestionIsMarked(t *testing.T) {
	workspace := t.TempDir()
	ctx := WithWorkingDir(context.Background(), workspace)
	off := func(context.Context) bool { return false }
	resolver := ComposeResolver(
		func(context.Context, string, string, bool) Decision { return DecisionAllow },
		Policy{SkipAll: off, SkipTools: off, OutsideAllowed: func(context.Context, OutsideClass) bool { return false }},
	)
	other := filepath.Join(filepath.Dir(workspace), "other-project")
	for _, tc := range []struct {
		subject string
		gitDir  bool
	}{
		{".git/config", true},
		{filepath.Join(other, ".git", "config"), true},
		{"src/main.go", false},
		{filepath.Join(other, "main.go"), false},
	} {
		got := resolver(ctx, Query{Tool: "write_file", Subject: tc.subject, Static: true, Class: OutsideWrite})
		if got.GitDir != tc.gitDir {
			t.Errorf("%s: GitDir = %v, want %v", tc.subject, got.GitDir, tc.gitDir)
		}
	}
}

// git reads its per-user configuration in every repository, so a write
// there is a write into every .git at once.
func TestGitUserConfigIsAGitDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("HOME spelling")
	}
	real := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(real, home); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	ctx := WithWorkingDir(context.Background(), t.TempDir())

	for _, path := range []string{
		filepath.Join(home, ".gitconfig"), filepath.Join(home, ".GITCONFIG"),
		filepath.Join(real, ".gitconfig"),
		filepath.Join(home, ".config", "git", "config"),
		filepath.Join(home, ".config", "git", "attributes"),
		filepath.Join(real, ".config", "git", "ignore"),
	} {
		if !IntoGitDir(ctx, path) {
			t.Errorf("IntoGitDir(%q) = false: git reads it in every repository", path)
		}
	}
	for _, path := range []string{
		filepath.Join(home, ".gitconfig.bak"), filepath.Join(home, "notes.md"),
		filepath.Join(home, ".config", "gitx", "config"), filepath.Join(home, ".config", "git"+"hub", "hosts.yml"),
	} {
		if IntoGitDir(ctx, path) {
			t.Errorf("IntoGitDir(%q) = true, want false", path)
		}
	}

	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "custom.cfg"))
	for _, path := range []string{
		filepath.Join(home, "xdg", "git", "config"), filepath.Join(home, "custom.cfg"),
	} {
		if !IntoGitDir(ctx, path) {
			t.Errorf("IntoGitDir(%q) = false with XDG_CONFIG_HOME and GIT_CONFIG_GLOBAL set", path)
		}
	}
}
