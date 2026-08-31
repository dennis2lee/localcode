package agent

import (
	"context"
	"strings"
	"testing"

	"localcode/internal/prompt"
)

// Where the model thinks it is.
//
// Nothing in the prompt named the directory, so a model learned it from
// tool output: a pwd, a glob result, a path in an earlier answer. That
// knowledge lived in the conversation history, and moving the workspace
// does not rewrite history. The model went on prefixing every shell
// command with "cd <the old path> &&", and files appeared in the project
// the person had just left, while the daemon answered every question about
// the workspace with the new one. The two never disagreed: only the model
// was working from a remembered directory.
//
// Bash is why nothing caught it. The workspace boundary is a check on
// paths, and a shell command is not a path.

func systemPromptFor(t *testing.T, loop *Loop, sessionID string) string {
	t.Helper()
	ctx := context.Background()
	cfg := loop.agentConfig(ctx, "general-purpose")
	actx := loop.activationFor(ctx, sessionID, "general-purpose", cfg, "strong",
		loop.Config.Profiles["strong"], 0, []string{"write_file"})
	return prompt.Assemble(loop.promptAssets(), actx).SystemText()
}

func TestThePromptSaysWhereTheConversationIsWorking(t *testing.T) {
	srv, _ := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)
	dir := t.TempDir()
	if _, err := loop.Store.CreateSessionIn("s1", "", "general-purpose", dir, true); err != nil {
		t.Fatal(err)
	}

	got := systemPromptFor(t, loop, "s1")
	if !strings.Contains(got, dir) {
		t.Fatalf("the prompt never names the directory, so the model can only learn it from tool output:\n%s", got)
	}
	if !strings.Contains(got, "Relative paths") {
		t.Errorf("the prompt names the directory without saying what it means:\n%s", got)
	}
	// The warning is the half that matters after a move: the history still
	// holds the old paths, and the model has no other way to know they are
	// stale.
	if !strings.Contains(got, "earlier in this conversation") {
		t.Errorf("nothing warns that a remembered path may be the old workspace:\n%s", got)
	}
}

func TestTheStatedWorkspaceFollowsAMove(t *testing.T) {
	srv, _ := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)
	old, neu := t.TempDir(), t.TempDir()
	loop.Store.CreateSessionIn("s1", "", "general-purpose", old, true)

	if got := systemPromptFor(t, loop, "s1"); !strings.Contains(got, old) {
		t.Fatalf("the prompt does not name the first directory")
	}
	if _, err := loop.Store.SetWorkspace("s1", neu); err != nil {
		t.Fatal(err)
	}

	got := systemPromptFor(t, loop, "s1")
	if !strings.Contains(got, neu) {
		t.Errorf("after the move the prompt still does not name the new directory:\n%s", got)
	}
	if strings.Contains(got, old) {
		t.Errorf("after the move the prompt still names the old directory, which is the whole defect:\n%s", got)
	}
}

// Two conversations in two projects on one daemon get two different
// answers, which is the property per-session workspaces exist for.
func TestTwoSessionsAreToldTheirOwnDirectories(t *testing.T) {
	srv, _ := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)
	a, b := t.TempDir(), t.TempDir()
	loop.Store.CreateSessionIn("s1", "", "general-purpose", a, true)
	loop.Store.CreateSessionIn("s2", "", "general-purpose", b, true)

	if got := systemPromptFor(t, loop, "s1"); !strings.Contains(got, a) || strings.Contains(got, b) {
		t.Errorf("session one was told the wrong directory")
	}
	if got := systemPromptFor(t, loop, "s2"); !strings.Contains(got, b) || strings.Contains(got, a) {
		t.Errorf("session two was told the wrong directory")
	}
}

// A session with no recorded directory says nothing rather than inventing
// one: the sessions that predate per-session workspaces have none, and a
// made-up path is worse than silence.
func TestASessionWithNoDirectoryIsToldNothing(t *testing.T) {
	srv, _ := smartServer(t)
	defer srv.Close()
	loop := newSmartLoop(t, srv.URL)
	loop.Store.CreateSession("s1", "", "general-purpose", true)
	loop.SetProjectDir("")

	if got := systemPromptFor(t, loop, "s1"); strings.Contains(got, "You are working in") {
		t.Errorf("a session with no directory was told it has one:\n%s", got)
	}
}
