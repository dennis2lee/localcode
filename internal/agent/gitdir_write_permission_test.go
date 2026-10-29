package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"localcode/internal/config"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// A write into .git is asked though a rule allows writes, and an "allow
// for session" answer covers that path again. The first half is the guard
// (tools.IntoGitDir); the second is what keeps it from asking forever
// about a file somebody already said yes to, and it lives in the broker,
// so only a test through both shows them connected.
func TestAWriteIntoGitIsAskedOnceForTheSession(t *testing.T) {
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
		Permissions:    config.Permissions{"write_file": config.ToolPermission{Flat: config.DecisionAllow}},
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
	asked := func(path, scope string) {
		t.Helper()
		before := len(mustEvents(t, store, sid))
		res := write(path)
		broker.Resolve(waitForPermissionID(t, store, sid, before), true, scope)
		if r := <-res; r.IsError {
			t.Errorf("approved write %s: %s", path, r.Content)
		}
	}

	unasked("src/main.go")
	asked(".git/config", ScopeSession)
	unasked(".git/config")
	asked(".git/hooks/pre-commit", ScopeOnce)
}
