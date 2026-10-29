package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// A write into .git is asked though a rule allows writes, and asked every
// time: the question says it is a one-use one, and an answer of "for the
// session" sent anyway grants nothing. The guard is in tools and the
// one-use handling in the broker, so only a test through both shows them
// connected.
func TestAWriteIntoGitIsAskedEveryTime(t *testing.T) {
	r := newGitWriteRig(t, true)
	r.unasked("src/main.go")
	if _, once := r.asked(".git/config", ScopeSession); !once {
		t.Error("the .git question was not marked once, so a client offers \"for the session\" and \"always\"")
	}
	r.asked(".git/config", ScopeOnce)
	r.asked(".git/hooks/pre-commit", ScopeOnce)
}

// A grant is kept by how a path is spelled. One given for an ordinary file
// must not cover that file once it has become a link into .git, which
// git's own allowed commands can do (hash-object, update-index, checkout).
func TestAGrantForAFileDoesNotCoverItAsALinkIntoGit(t *testing.T) {
	r := newGitWriteRig(t, false)
	r.asked("notes.txt", ScopeSession)
	r.unasked("notes.txt")
	notes := filepath.Join(r.project, "notes.txt")
	if err := os.Remove(notes); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(r.project, ".git", "config"), notes); err != nil {
		t.Fatal(err)
	}
	r.asked("notes.txt", ScopeOnce)
}

// "Allow writes in this directory", given for another project's file,
// does not cover that project's .git. The .git question is a plain one,
// so no answer to it covers a directory either.
func TestAnApprovedDirectoryDoesNotCoverItsGitDir(t *testing.T) {
	r := newGitWriteRig(t, true)
	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.asked(filepath.Join(other, "main.go"), ScopeOutsideDir); got != "write" {
		t.Fatalf("a write to another project was asked as %q, want a boundary question", got)
	}
	r.unasked(filepath.Join(other, "notes.md"))
	if got, _ := r.asked(filepath.Join(other, ".git", "config"), ScopeOnce); got != "" {
		t.Errorf("a write into the other project's .git was asked as a boundary question (%q); a directory answer could cover it", got)
	}
}

type gitWriteRig struct {
	project string
	unasked func(path string)
	asked   func(path, scope string) (outside string, once bool)
}

// newGitWriteRig is one session over a project with a .git, with
// write_file allowed by a rule when allowWrites is set.
func newGitWriteRig(t *testing.T, allowWrites bool) gitWriteRig {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}

	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	const sid = "s1"
	if _, err := store.CreateSessionIn(sid, "", "general-purpose", project, true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1"}},
		Profiles:       map[string]config.Profile{"balanced": {Provider: "local", Model: "m"}},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "balanced"}},
		DefaultProfile: "balanced",
	}
	if allowWrites {
		cfg.Permissions = config.Permissions{"write_file": config.ToolPermission{Flat: config.DecisionAllow}}
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid config: %v", err)
	}

	broker := NewPermissionBroker(store)
	policy := NewPermissionPolicy(store, cfg)
	broker.SetPolicy(policy, func(string) {})
	reg := tools.NewRegistry(broker.Func())
	reg.Resolver = tools.ComposeResolver(
		func(ctx context.Context, toolName, subject string, static bool) tools.Decision {
			return tools.Decision(cfg.ResolvePermissionFor(ctx, toolName, subject, static))
		},
		policy.ToolsPolicy(),
	)
	reg.Register(tools.WriteFile{})

	write := func(path string) chan tools.Result {
		out := make(chan tools.Result, 1)
		go func() {
			ctx := tools.WithWorkingDir(WithSessionID(context.Background(), sid), project)
			input, _ := json.Marshal(map[string]string{"path": path, "content": "x\n"})
			out <- reg.Call(ctx, "write_file", input, "")
		}()
		return out
	}
	unasked := func(path string) {
		t.Helper()
		before := len(mustEvents(t, store, sid))
		select {
		case res := <-write(path):
			if res.IsError {
				t.Errorf("write %s: %s", path, res.Content)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("write %s asked a question", path)
		}
		if n := len(mustEvents(t, store, sid)) - before; n != 0 {
			t.Errorf("write %s wrote %d event(s); it should have asked nothing", path, n)
		}
	}
	// asked answers the question the write raises and reports which kind
	// it was: "write" for a boundary question, "" for a plain one, and
	// whether it was marked as asked every time.
	asked := func(path, scope string) (string, bool) {
		t.Helper()
		before := len(mustEvents(t, store, sid))
		res := write(path)
		id := waitForPermissionID(t, store, sid, before)
		var outside string
		var once bool
		for _, ev := range mustEvents(t, store, sid)[before:] {
			if ev.Type == events.TypePermissionRequest {
				outside, _ = ev.Data["outside"].(string)
				once, _ = ev.Data["once"].(bool)
			}
		}
		broker.Resolve(id, true, scope)
		if r := <-res; r.IsError {
			t.Errorf("approved write %s: %s", path, r.Content)
		}
		return outside, once
	}
	return gitWriteRig{project: project, unasked: unasked, asked: asked}
}
