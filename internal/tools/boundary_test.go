package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// A tool that does not touch a path has no boundary to cross. bash is
// the member that matters: a shell command is not a path, "cd /etc &&
// cat passwd" cannot be judged by looking at it, and a guard that
// pretended otherwise would be claiming a protection it does not have.
func TestOnlyPathToolsHaveABoundary(t *testing.T) {
	workspace := t.TempDir()
	ctx := WithWorkingDir(context.Background(), workspace)
	resolver := ComposeResolver(
		func(context.Context, string, string, bool) Decision { return DecisionAllow },
		Policy{
			SkipAll:        func(context.Context) bool { return false },
			SkipTools:      func(context.Context) bool { return false },
			OutsideAllowed: func(context.Context, OutsideClass) bool { return false },
		},
	)
	got := resolver(ctx, Query{Tool: "bash", Subject: "cat /etc/passwd", Class: OutsideNone})
	if got.Decision != DecisionAllow || got.Outside != OutsideNone {
		t.Errorf("bash = %+v, want a plain allow: the boundary has nothing to say about a shell command", got)
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
	// a read through that link becomes a boundary question.
	if got := askOutside(ctx, "link", OutsideRead); got.Decision != DecisionAsk {
		t.Errorf("read through a dangling external symlink = %q, want ask", got.Decision)
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

// R11N1. Following a dangling link and peeling an ordinary missing
// component are different operations, and they shared a counter: a path
// more than 64 components deep exhausted a *link* budget without
// following a single link, so an ordinary new directory tree entirely
// inside the workspace was classified outside and escalated to a
// question. Only followed links are bounded now.
func TestADeepMissingPathInsideTheWorkspaceIsStillInside(t *testing.T) {
	workspace := t.TempDir()
	ctx := WithWorkingDir(context.Background(), workspace)

	// Well past the link budget, no symlink anywhere, every component
	// lexically inside. A generated tree looks like this.
	deep := strings.Repeat("d/", 200) + "generated.go"
	if OutsideWorkspace(ctx, deep) {
		t.Error("a deep missing path inside the workspace was classified outside")
	}
	if got := askOutside(ctx, deep, OutsideRead); got.Decision != DecisionAllow {
		t.Errorf("a deep path inside the workspace = %q, want it left alone", got.Decision)
	}

	// The same depth aimed out of the workspace is still outside: the
	// fix must not have turned depth into an escape.
	if !OutsideWorkspace(ctx, "../"+strings.Repeat("d/", 200)+"generated.go") {
		t.Error("a deep missing path outside the workspace was classified inside")
	}

	// And the link budget still bounds links. A chain of dangling links
	// longer than the budget is unresolvable, which is outside.
	if runtime.GOOS == "windows" {
		return
	}
	for i := 0; i < maxDanglingLinkHops+2; i++ {
		from := filepath.Join(workspace, fmt.Sprintf("link%d", i))
		to := filepath.Join(workspace, fmt.Sprintf("link%d", i+1))
		if err := os.Symlink(to, from); err != nil {
			t.Fatalf("symlink: %v", err)
		}
	}
	if !OutsideWorkspace(ctx, "link0") {
		t.Error("a link chain longer than the hop budget was classified inside")
	}
}

// The whole pipeline, stated as a table, because the interaction between
// the four switches is the feature and everything about it is easy to get
// subtly wrong.
//
// The row that names the design is skip_tools with a write outside: the
// blanket that stops the interruptions still stops at the edge of the
// project. Before there was one blanket, and turning it on to stop being
// asked about this project also stopped being asked about every other
// one on the machine.
func TestThePermissionPipeline(t *testing.T) {
	workspace := t.TempDir()
	ctx := WithWorkingDir(context.Background(), workspace)

	// A deny rule on anything that looks like a private key, an allow on
	// everything else: the shipped guards in miniature.
	rules := func(_ context.Context, _, subject string, _ bool) Decision {
		if strings.Contains(subject, "id_rsa") {
			return DecisionDeny
		}
		return DecisionAllow
	}
	outside := filepath.Join(filepath.Dir(workspace), "other-project", "main.go")
	inside := "main.go"

	for _, tt := range []struct {
		name                      string
		skipAll, skipTools        bool
		readOutside, writeOutside bool
		tool, subject             string
		class                     OutsideClass
		want                      Decision
		wantOutside               OutsideClass
	}{
		// Nothing on. Inside is ordinary, outside is a question, in both
		// directions.
		{"inside, nothing on", false, false, false, false, "read_file", inside, OutsideRead, DecisionAllow, OutsideNone},
		{"read outside, nothing on", false, false, false, false, "read_file", outside, OutsideRead, DecisionAsk, OutsideRead},
		{"write outside, nothing on", false, false, false, false, "write_file", outside, OutsideWrite, DecisionAsk, OutsideWrite},

		// The example this feature was specified with: skip_tools on and
		// everything else off, and a grep reaching outside still asks.
		{"grep outside under skip_tools", false, true, false, false, "grep", outside, OutsideRead, DecisionAsk, OutsideRead},
		{"write outside under skip_tools", false, true, false, false, "write_file", outside, OutsideWrite, DecisionAsk, OutsideWrite},
		{"write inside under skip_tools", false, true, false, false, "write_file", inside, OutsideWrite, DecisionAllow, OutsideNone},

		// The two halves are separate. Allowing reads out there says
		// nothing about writes.
		{"read outside with read allowed", false, false, true, false, "read_file", outside, OutsideRead, DecisionAllow, OutsideNone},
		{"write outside with read allowed", false, false, true, false, "write_file", outside, OutsideWrite, DecisionAsk, OutsideWrite},
		{"write outside with write allowed", false, false, false, true, "write_file", outside, OutsideWrite, DecisionAllow, OutsideNone},

		// skip_all is the only one that crosses the boundary.
		{"write outside under skip_all", true, false, false, false, "write_file", outside, OutsideWrite, DecisionAllow, OutsideNone},

		// A deny is a rule somebody wrote. Nothing below it softens it,
		// including both skips, and including being inside the project.
		{"deny inside", false, false, false, false, "read_file", "id_rsa", OutsideRead, DecisionDeny, OutsideNone},
		{"deny under skip_all", true, false, false, false, "read_file", "id_rsa", OutsideRead, DecisionDeny, OutsideNone},
		{"deny outside", false, false, false, false, "read_file", filepath.Join(filepath.Dir(workspace), "id_rsa"), OutsideRead, DecisionDeny, OutsideNone},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resolver := ComposeResolver(rules, Policy{
				SkipAll:   func(context.Context) bool { return tt.skipAll },
				SkipTools: func(context.Context) bool { return tt.skipTools },
				OutsideAllowed: func(_ context.Context, c OutsideClass) bool {
					return (c == OutsideRead && tt.readOutside) || (c == OutsideWrite && tt.writeOutside)
				},
			})
			got := resolver(ctx, Query{Tool: tt.tool, Subject: tt.subject, Static: true, Class: tt.class})
			if got.Decision != tt.want {
				t.Errorf("decision = %q, want %q", got.Decision, tt.want)
			}
			if got.Outside != tt.wantOutside {
				t.Errorf("outside class = %v, want %v (a question that cannot say why it is being asked is one people answer without reading)", got.Outside, tt.wantOutside)
			}
			if tt.wantOutside != OutsideNone && got.Dir == "" {
				t.Error("a boundary question carried no directory, so there is nothing an \"allow this directory\" answer could cover")
			}
		})
	}
}

// askOutside runs one subject through the pipeline with every switch off,
// which is the state the boundary is the only thing speaking in.
func askOutside(ctx context.Context, subject string, class OutsideClass) Outcome {
	return ComposeResolver(
		func(context.Context, string, string, bool) Decision { return DecisionAllow },
		Policy{
			SkipAll:        func(context.Context) bool { return false },
			SkipTools:      func(context.Context) bool { return false },
			OutsideAllowed: func(context.Context, OutsideClass) bool { return false },
		},
	)(ctx, Query{Tool: "read_file", Subject: subject, Static: true, Class: class})
}
