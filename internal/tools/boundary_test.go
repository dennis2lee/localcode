package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The boundary a coding agent is expected to respect. Not a prohibition —
// reading a system header or a file in another checkout is ordinary work —
// but worth being asked about once, which is the difference between an
// agent that stays where it was pointed and one that is discovered to have
// been somewhere else.
func TestOutsideWorkspace(t *testing.T) {
	dir := t.TempDir()
	ctx := WithWorkingDir(context.Background(), dir)

	inside := []string{
		"main.go", "src/app/main.go", "./x", "a/../b",
		filepath.Join(dir, "main.go"),
		filepath.Join(dir, "deep", "nested", "file.txt"),
	}
	for _, p := range inside {
		if OutsideWorkspace(ctx, p) {
			t.Errorf("%q was called outside the workspace", p)
		}
	}

	outside := []string{
		"/etc/passwd", "../sibling/main.go", "../../etc/hosts",
		filepath.Join(filepath.Dir(dir), "other", "main.go"),
	}
	for _, p := range outside {
		if !OutsideWorkspace(ctx, p) {
			t.Errorf("%q was called inside the workspace", p)
		}
	}
}

// No directory on the context means no boundary to be outside of, which is
// every bare Loop in a test and the daemon before a workspace is set.
func TestWithNoWorkspaceNothingIsOutsideIt(t *testing.T) {
	if OutsideWorkspace(context.Background(), "/etc/passwd") {
		t.Error("a path was called outside a workspace that does not exist")
	}
	dir := t.TempDir()
	if OutsideWorkspace(WithWorkingDir(context.Background(), dir), "") {
		t.Error("an empty path was treated as the filesystem root")
	}
}

// Escalate allow to ask and nothing else. A deny is a rule somebody wrote
// and is not softened by where the file happens to be.
func TestBoundaryDecisionOnlyEscalates(t *testing.T) {
	dir := t.TempDir()
	ctx := WithWorkingDir(context.Background(), dir)

	if got := BoundaryDecision(ctx, DecisionAllow, "/etc/passwd", true); got != DecisionAsk {
		t.Errorf("allow outside the workspace = %q, want ask", got)
	}
	if got := BoundaryDecision(ctx, DecisionAllow, "main.go", true); got != DecisionAllow {
		t.Errorf("allow inside the workspace = %q, want allow", got)
	}
	if got := BoundaryDecision(ctx, DecisionDeny, "/etc/passwd", true); got != DecisionDeny {
		t.Errorf("deny outside the workspace = %q, want it left alone", got)
	}
	if got := BoundaryDecision(ctx, DecisionAsk, "main.go", true); got != DecisionAsk {
		t.Errorf("ask = %q, want it left alone", got)
	}
	// Off is off: this is part of a feature that is opted into.
	if got := BoundaryDecision(ctx, DecisionAllow, "/etc/passwd", false); got != DecisionAllow {
		t.Errorf("with the boundary off = %q, want the previous behaviour", got)
	}
}

// Item 15. The boundary used to be lexical, so a symlink inside the
// workspace pointing out of it was "inside" as far as the check was
// concerned — the exact hole docs/IMPROVEMENTS.md carried as the largest
// item left from the 2026-08 review. The comparison is now between
// physical paths: a link is judged by where it leads.
func TestASymlinkOutOfTheWorkspaceIsOutside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs a privilege most Windows test runs do not have")
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	elsewhere := filepath.Join(root, "elsewhere")
	for _, d := range []string{workspace, elsewhere} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(elsewhere, "secret.txt"), []byte("s"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(workspace, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	ctx := WithWorkingDir(context.Background(), workspace)

	// Through the link, an existing file and one about to be created:
	// both land elsewhere, whatever the path says.
	for _, p := range []string{"link/secret.txt", "link/new.txt", filepath.Join(workspace, "link", "secret.txt")} {
		if !OutsideWorkspace(ctx, p) {
			t.Errorf("%q leads out of the workspace and was called inside", p)
		}
	}
	// A link that stays inside is still inside, and so is ordinary work
	// beside it — resolution must not turn the whole workspace into a
	// question.
	if err := os.Symlink(filepath.Join(workspace), filepath.Join(workspace, "self")); err == nil {
		if OutsideWorkspace(ctx, "self/main.go") {
			t.Error("a link that stays inside the workspace was called outside")
		}
	}
	for _, p := range []string{"main.go", "sub/new.go"} {
		if OutsideWorkspace(ctx, p) {
			t.Errorf("%q was called outside its own workspace", p)
		}
	}
}

// A workspace that is itself reached through a symlink must not make its
// own contents look foreign. macOS makes this the common case: /tmp is a
// link to /private/tmp, so every temp-dir workspace exercises it.
func TestAWorkspaceBehindASymlinkStillContainsItsOwnFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs a privilege most Windows test runs do not have")
	}
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(filepath.Join(real, "src"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	linked := filepath.Join(root, "linked")
	if err := os.Symlink(real, linked); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	ctx := WithWorkingDir(context.Background(), linked)

	if OutsideWorkspace(ctx, "src/main.go") {
		t.Error("a file inside a symlinked workspace was called outside it")
	}
	if OutsideWorkspace(ctx, filepath.Join(real, "src", "main.go")) {
		t.Error("the physical spelling of a workspace file was called outside it")
	}
	if !OutsideWorkspace(ctx, "/etc/passwd") {
		t.Error("/etc/passwd was called inside a symlinked workspace")
	}
}

// Unresolvable is not the same as safe. A path that cannot be resolved —
// here, a symlink loop — is treated as outside, which costs one question
// rather than one blind allow.
func TestAnUnresolvablePathIsOutside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs a privilege most Windows test runs do not have")
	}
	workspace := t.TempDir()
	loop := filepath.Join(workspace, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	ctx := WithWorkingDir(context.Background(), workspace)
	if !OutsideWorkspace(ctx, "loop/file.txt") {
		t.Error("a path through a symlink loop was called inside the workspace")
	}
}

// R10N1. A symlink whose target does not exist yet reports ErrNotExist
// from EvalSymlinks exactly like an ordinary missing file, and treating
// it as one judged the path by where the link sits instead of where it
// points. That is the one direction a *write* boundary cannot get wrong:
// writing through the dangling link is what creates the external file.
func TestADanglingSymlinkToOutsideIsOutside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs a privilege most Windows test runs do not have")
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// The direct case from the review: link -> /outside/new.txt where
	// new.txt does not exist. The write would create it outside.
	if err := os.Symlink(filepath.Join(root, "outside", "new.txt"), filepath.Join(workspace, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	ctx := WithWorkingDir(context.Background(), workspace)
	if !OutsideWorkspace(ctx, "link") {
		t.Error("a dangling symlink to an external missing file was called inside")
	}
	// And the classification is what the permission layer actually sees:
	// an allow through that link escalates to ask.
	if got := BoundaryDecision(ctx, DecisionAllow, "link", true); got != DecisionAsk {
		t.Errorf("allow through a dangling external symlink = %q, want ask", got)
	}

	// A relative dangling link is followed relative to its own directory,
	// not the process's.
	if err := os.Symlink(filepath.Join("..", "outside2", "new.txt"), filepath.Join(workspace, "rel")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if !OutsideWorkspace(ctx, "rel") {
		t.Error("a relative dangling symlink out of the workspace was called inside")
	}

	// A dangling link that stays inside is still inside: the fix must not
	// turn every not-yet-written file behind a link into a question.
	if err := os.Symlink(filepath.Join(workspace, "future.txt"), filepath.Join(workspace, "soon")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if OutsideWorkspace(ctx, "soon") {
		t.Error("a dangling symlink that stays inside the workspace was called outside")
	}

	// A chain of dangling links that cycles never converges; unresolvable
	// is outside, not a hang and not an allow.
	if err := os.Symlink(filepath.Join(workspace, "b"), filepath.Join(workspace, "a")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(workspace, "a"), filepath.Join(workspace, "b")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if !OutsideWorkspace(ctx, "a") {
		t.Error("a cycle of symlinks was called inside the workspace")
	}
}
