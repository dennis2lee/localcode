package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The whole point: two sessions, two directories, at the same time.
//
// Before, the workspace was the process's own working directory, so there
// was exactly one of it. Two sessions in two projects could not both be
// right, and moving one moved the other.
func TestTwoDirectoriesAtOnce(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(a, "which.txt"), []byte("project A"), 0o644)
	os.WriteFile(filepath.Join(b, "which.txt"), []byte("project B"), 0o644)

	ctxA := WithWorkingDir(context.Background(), a)
	ctxB := WithWorkingDir(context.Background(), b)
	input := json.RawMessage(`{"path":"which.txt"}`)

	gotA := ReadFile{}.Execute(ctxA, input)
	gotB := ReadFile{}.Execute(ctxB, input)
	if gotA.IsError || gotB.IsError {
		t.Fatalf("read failed: %q / %q", gotA.Content, gotB.Content)
	}
	if !strings.Contains(gotA.Content, "project A") {
		t.Errorf("session A read %q", gotA.Content)
	}
	if !strings.Contains(gotB.Content, "project B") {
		t.Errorf("session B read %q, so the same relative path did not follow the session", gotB.Content)
	}
}

// A write lands in the session's directory, not wherever the daemon was
// started. This is the one that used to put a file in the wrong repository.
func TestWriteLandsInTheSessionsDirectory(t *testing.T) {
	dir := t.TempDir()
	ctx := WithWorkingDir(context.Background(), dir)

	res := WriteFile{}.Execute(ctx, json.RawMessage(`{"path":"sub/new.txt","content":"hello"}`))
	if res.IsError {
		t.Fatalf("write: %s", res.Content)
	}
	got, err := os.ReadFile(filepath.Join(dir, "sub", "new.txt"))
	if err != nil {
		t.Fatalf("the file is not in the session's directory: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q", got)
	}
}

// An absolute path is left exactly alone: the model named a specific place.
func TestAbsolutePathsAreNotRebased(t *testing.T) {
	elsewhere := t.TempDir()
	target := filepath.Join(elsewhere, "abs.txt")
	os.WriteFile(target, []byte("found"), 0o644)

	ctx := WithWorkingDir(context.Background(), t.TempDir())
	res := ReadFile{}.Execute(ctx, json.RawMessage(fmt.Sprintf(`{"path":%q}`, target)))
	if res.IsError || !strings.Contains(res.Content, "found") {
		t.Errorf("absolute read = %q", res.Content)
	}
}

// With no directory on the context, everything behaves exactly as it did
// before any of this existed — relative to the process. That is what keeps
// a Loop built without a workspace, and every existing caller, working.
func TestNoWorkingDirIsTheOldBehaviour(t *testing.T) {
	if got := resolve(context.Background(), "src/main.go"); got != "src/main.go" {
		t.Errorf("resolve without a directory = %q, want it untouched", got)
	}
}

// Searches answer in the terms they were asked in: a relative pattern gets
// relative results, which the model can hand straight back to read_file.
func TestSearchReportsPathsRelativeToTheSession(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "pkg"), 0o755)
	os.WriteFile(filepath.Join(dir, "pkg", "a.go"), []byte("package pkg\n// TODO: fix\n"), 0o644)
	ctx := WithWorkingDir(context.Background(), dir)

	globbed := Glob{}.Execute(ctx, json.RawMessage(`{"pattern":"**/*.go"}`))
	if globbed.IsError {
		t.Fatalf("glob: %s", globbed.Content)
	}
	if got := strings.TrimSpace(globbed.Content); got != filepath.Join("pkg", "a.go") {
		t.Errorf("glob = %q, want a path relative to the session's directory", got)
	}

	grepped := Grep{}.Execute(ctx, json.RawMessage(`{"pattern":"TODO"}`))
	if grepped.IsError {
		t.Fatalf("grep: %s", grepped.Content)
	}
	if strings.Contains(grepped.Content, dir) {
		t.Errorf("grep leaked the absolute workspace path into its output: %q", grepped.Content)
	}
	if !strings.Contains(grepped.Content, filepath.Join("pkg", "a.go")) {
		t.Errorf("grep = %q", grepped.Content)
	}
}

// A shell command runs in the session's directory too. This is the half of
// the workspace people notice first: `ls`, `git status` and every build
// command used to run wherever localcode itself was started.
func TestBashRunsInTheSessionsDirectory(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("x"), 0o644)
	ctx := WithWorkingDir(context.Background(), dir)

	res := Bash{}.Execute(ctx, json.RawMessage(`{"command":"ls"}`))
	if res.IsError {
		t.Fatalf("bash: %s", res.Content)
	}
	if !strings.Contains(res.Content, "marker.txt") {
		t.Errorf("ls ran somewhere else: %q", res.Content)
	}
}
