package tools

import (
	"context"
	"path/filepath"
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
