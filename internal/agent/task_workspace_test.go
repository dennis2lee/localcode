package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"localcode/internal/commands"
	"localcode/internal/config"
	"localcode/internal/hooks"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// Where delegated work happens.
//
// A conversation carries its own directory (see SessionDir), and every
// tool a turn runs resolves relative paths against it. A task session was
// created without one, so it fell through to the daemon's default, which
// is wherever the daemon was started. Any conversation that was somewhere
// else — a workspace switched from the header, a session reopened in the
// project it belongs to, a second client on the same daemon in a second
// checkout — delegated to agents that read and wrote in a different
// project from the one that asked, and said nothing about it.
//
// These tests ask the question at the only place that settles it: not what
// the session metadata says, but which directory the child's tools
// actually ran in.

// pwdTool answers with the directory its turn resolves paths against.
type pwdTool struct{ seen *string }

func (t pwdTool) Name() string        { return "pwd" }
func (t pwdTool) Description() string { return "report the working directory" }
func (t pwdTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t pwdTool) RequiresPermission(json.RawMessage) bool { return false }
func (t pwdTool) Execute(ctx context.Context, _ json.RawMessage) tools.Result {
	*t.seen = tools.WorkingDir(ctx)
	return tools.Result{Content: *t.seen}
}

// pwdTurns is one delegated turn: call the tool, then answer.
func pwdTurns() [][]provider.StreamEvent {
	return [][]provider.StreamEvent{
		{
			{Type: provider.EventToolUseStart, ToolUseID: "call_1", ToolName: "pwd"},
			{Type: provider.EventToolUseEnd, ToolUseID: "call_1", ToolInput: json.RawMessage(`{}`)},
			{Type: provider.EventMessageStop, StopReason: "tool_use"},
		},
		{
			{Type: provider.EventTextDelta, TextDelta: "done"},
			{Type: provider.EventMessageStop, StopReason: "end_turn"},
		},
	}
}

// taskWorkspaceLoop is a loop whose default directory is deliberately not
// any session's, so a child that inherits nothing is visibly wrong rather
// than accidentally right.
func taskWorkspaceLoop(t *testing.T, turns int) (*Loop, *string, string) {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	var seen string
	reg := tools.NewRegistry(nil)
	reg.Register(pwdTool{seen: &seen})

	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1"}},
		Profiles:       map[string]config.Profile{"balanced": {Provider: "local", Model: "m"}},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "balanced"}},
		DefaultProfile: "balanced",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}
	var script [][]provider.StreamEvent
	for i := 0; i < turns; i++ {
		script = append(script, pwdTurns()...)
	}
	p := &scriptedProvider{turns: script}

	daemonDir := t.TempDir()
	loop := New(store, reg, map[string]provider.Provider{"local": p}, cfg)
	loop.ProjectDir = daemonDir
	return loop, &seen, daemonDir
}

// childOf returns the one task session under parentID.
func childOf(t *testing.T, loop *Loop, parentID string) session.Session {
	t.Helper()
	children := loop.Store.Children(parentID)
	if len(children) != 1 {
		t.Fatalf("session %s has %d children, want 1", parentID, len(children))
	}
	return children[0]
}

// Both ways of delegating, because they create the child session in two
// separate places and only one of them being fixed is the same bug with a
// narrower reproduction.
func TestADelegatedTaskWorksInTheWorkspaceOfTheSessionThatLaunchedIt(t *testing.T) {
	for _, tt := range []struct {
		name  string
		spawn func(t *testing.T, tm *TaskManager, ctx context.Context, parentID string)
	}{
		{"background", func(t *testing.T, tm *TaskManager, ctx context.Context, parentID string) {
			id, err := tm.SpawnBackground(ctx, parentID, "general-purpose", "go", "")
			if err != nil {
				t.Fatalf("SpawnBackground: %v", err)
			}
			if _, runErr, done := tm.Wait(ctx, id); !done || runErr != nil {
				t.Fatalf("task did not finish cleanly: done=%v err=%v", done, runErr)
			}
		}},
		{"synchronous", func(t *testing.T, tm *TaskManager, ctx context.Context, parentID string) {
			if _, err := tm.SpawnSync(ctx, parentID, "general-purpose", "go"); err != nil {
				t.Fatalf("SpawnSync: %v", err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			loop, seen, daemonDir := taskWorkspaceLoop(t, 1)
			tm := NewTaskManager(context.Background(), loop, 5)

			projectDir := t.TempDir()
			const parentID = "s-parent"
			if _, err := loop.Store.CreateSessionIn(parentID, "", "general-purpose", projectDir, true); err != nil {
				t.Fatalf("create session: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			tt.spawn(t, tm, ctx, parentID)

			if *seen != projectDir {
				t.Errorf("the task's tools ran in %q; the conversation that launched it works in %q", *seen, projectDir)
			}
			if *seen == daemonDir {
				t.Errorf("the task ran in the daemon's own start directory (%q) instead of the project it was launched from", daemonDir)
			}

			child := childOf(t, loop, parentID)
			if child.Workspace != projectDir {
				t.Errorf("task session workspace = %q, want %q; without it the directory is lost across a restart", child.Workspace, projectDir)
			}

			// Stamped, not looked up: the parent moving afterwards moves
			// the parent. A task is finishing instructions it was given in
			// a particular directory.
			if _, err := loop.Store.SetWorkspace(parentID, t.TempDir()); err != nil {
				t.Fatalf("SetWorkspace: %v", err)
			}
			if got := loop.SessionDir(child.ID); got != projectDir {
				t.Errorf("after the parent moved, the task's directory = %q, want the %q it was launched in", got, projectDir)
			}
		})
	}
}

// Delegation nests, so inheritance has to. The middle link is what makes
// this work with no code of its own: a task that delegates is by then a
// session with a workspace of its own to pass down.
func TestATaskSpawnedByATaskStaysInTheSameProject(t *testing.T) {
	loop, seen, daemonDir := taskWorkspaceLoop(t, 2)
	tm := NewTaskManager(context.Background(), loop, 5)

	projectDir := t.TempDir()
	const parentID = "s-parent"
	if _, err := loop.Store.CreateSessionIn(parentID, "", "general-purpose", projectDir, true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	childID, err := tm.SpawnBackground(ctx, parentID, "general-purpose", "go", "")
	if err != nil {
		t.Fatalf("SpawnBackground: %v", err)
	}
	if _, runErr, done := tm.Wait(ctx, childID); !done || runErr != nil {
		t.Fatalf("child did not finish cleanly: done=%v err=%v", done, runErr)
	}

	grandID, err := tm.SpawnBackground(ctx, childID, "general-purpose", "go", "")
	if err != nil {
		t.Fatalf("SpawnBackground (nested): %v", err)
	}
	if _, runErr, done := tm.Wait(ctx, grandID); !done || runErr != nil {
		t.Fatalf("grandchild did not finish cleanly: done=%v err=%v", done, runErr)
	}

	if *seen != projectDir {
		t.Errorf("a task two levels down ran in %q, want %q", *seen, projectDir)
	}
	if *seen == daemonDir {
		t.Errorf("the nested task fell back to the daemon's start directory %q", daemonDir)
	}
}

// A session that never recorded a workspace — one created before the
// workspace was per-session, or by a caller that had none to give — still
// resolves to the daemon's default, and that resolved answer is what the
// child is given. Stamping it rather than letting the child fall through
// too is what keeps a running task where it started when the default
// moves under it.
func TestATaskUnderASessionWithNoWorkspaceGetsTheDaemonDefault(t *testing.T) {
	loop, seen, daemonDir := taskWorkspaceLoop(t, 1)
	tm := NewTaskManager(context.Background(), loop, 5)

	const parentID = "s-parent"
	if _, err := loop.Store.CreateSession(parentID, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id, err := tm.SpawnBackground(ctx, parentID, "general-purpose", "go", "")
	if err != nil {
		t.Fatalf("SpawnBackground: %v", err)
	}
	if _, runErr, done := tm.Wait(ctx, id); !done || runErr != nil {
		t.Fatalf("task did not finish cleanly: done=%v err=%v", done, runErr)
	}

	if *seen != daemonDir {
		t.Errorf("the task ran in %q, want the daemon default %q its parent resolves to", *seen, daemonDir)
	}
	child := childOf(t, loop, parentID)
	if child.Workspace != daemonDir {
		t.Errorf("task session workspace = %q, want the resolved default %q rather than nothing", child.Workspace, daemonDir)
	}

	loop.SetProjectDir(t.TempDir())
	if got := loop.SessionDir(child.ID); got != daemonDir {
		t.Errorf("after the default moved, the task's directory = %q, want the %q it was launched in", got, daemonDir)
	}
}

// The same claim, made by a slash command rather than by a tool: @file
// reads a file and !`cmd` runs a shell command, and both mean the project
// this conversation is in.
func TestACustomCommandExpandsInTheSessionsWorkspace(t *testing.T) {
	var lastBody string
	model := mockChatServer(t, &lastBody)
	defer model.Close()

	cmds := []commands.Command{
		{Name: "show", Body: "notes:\n@note.md\nmarker: !`cat marker.txt`"},
	}
	loop, store := newCustomCommandTestLoop(t, model.URL, cmds)
	daemonDir := loop.GetProjectDir()
	if err := os.WriteFile(filepath.Join(daemonDir, "note.md"), []byte("the wrong project's notes"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(daemonDir, "marker.txt"), []byte("wrong-project"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "note.md"), []byte("the right project's notes"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "marker.txt"), []byte("right-project"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	const sid = "s1"
	if _, err := store.CreateSessionIn(sid, "", "general-purpose", projectDir, true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/show"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if !strings.Contains(lastBody, "the right project's notes") {
		t.Errorf("@file read the wrong project's copy; request body: %s", lastBody)
	}
	if strings.Contains(lastBody, "the wrong project's notes") {
		t.Errorf("@file resolved against the daemon's start directory; request body: %s", lastBody)
	}
	if !strings.Contains(lastBody, "right-project") || strings.Contains(lastBody, "wrong-project") {
		t.Errorf("the embedded shell command ran in the wrong directory; request body: %s", lastBody)
	}
}

// hookPwd reads back the directory a "pwd > file" hook recorded, with
// both sides resolved: a temp dir on macOS is reached through
// /var -> /private/var and pwd reports what it resolves to.
func hookPwd(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the hook did not run (no %s): %v", path, err)
	}
	got := strings.TrimSpace(string(data))
	if resolved, err := filepath.EvalSymlinks(got); err == nil {
		return resolved
	}
	return got
}

func resolved(t *testing.T, dir string) string {
	t.Helper()
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		return r
	}
	return dir
}

// A hook is a shell command, so where it runs decides what it sees. Fired
// during a delegated task's tool call, this one has to land in the
// project the conversation is in — which needs both halves: the task
// inheriting the workspace, and the hook being told about it rather than
// running wherever the daemon was started.
func TestAToolHookRunsInTheProjectTheTurnBelongsTo(t *testing.T) {
	loop, _, daemonDir := taskWorkspaceLoop(t, 1)
	tm := NewTaskManager(context.Background(), loop, 5)

	out := filepath.Join(t.TempDir(), "where")
	loop.Tools.Hooks = hooks.Config{hooks.EventPreToolUse: {{Command: "pwd > " + out}}}

	projectDir := t.TempDir()
	const parentID = "s-parent"
	if _, err := loop.Store.CreateSessionIn(parentID, "", "general-purpose", projectDir, true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id, err := tm.SpawnBackground(ctx, parentID, "general-purpose", "go", "")
	if err != nil {
		t.Fatalf("SpawnBackground: %v", err)
	}
	if _, runErr, done := tm.Wait(ctx, id); !done || runErr != nil {
		t.Fatalf("task did not finish cleanly: done=%v err=%v", done, runErr)
	}

	got := hookPwd(t, out)
	if got != resolved(t, projectDir) {
		t.Errorf("the pre_tool_use hook ran in %q, want the session's project %q", got, projectDir)
	}
	if got == resolved(t, daemonDir) {
		t.Errorf("the hook ran in the daemon's own start directory %q", daemonDir)
	}
}

// The same for the hooks that are not about a tool at all: they are
// events of a turn, and a turn belongs to a session that is somewhere.
func TestATurnHookRunsInTheProjectTheSessionBelongsTo(t *testing.T) {
	loop, _, daemonDir := taskWorkspaceLoop(t, 1)

	out := filepath.Join(t.TempDir(), "where")
	loop.Config.Hooks = hooks.Config{hooks.EventUserPromptSubmit: {{Command: "pwd > " + out}}}

	projectDir := t.TempDir()
	const sid = "s1"
	if _, err := loop.Store.CreateSessionIn(sid, "", "general-purpose", projectDir, true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "hi"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	got := hookPwd(t, out)
	if got != resolved(t, projectDir) {
		t.Errorf("the user_prompt_submit hook ran in %q, want the session's project %q", got, projectDir)
	}
	if got == resolved(t, daemonDir) {
		t.Errorf("the hook ran in the daemon's own start directory %q", daemonDir)
	}
}
