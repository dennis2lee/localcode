package tools

import (
	"context"
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
}
