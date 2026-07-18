package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/hooks"
)

// fakeTool is a minimal Tool for exercising Registry.Call's permission
// gating without depending on any real tool's side effects.
type fakeTool struct {
	name      string
	needsPerm bool
	executed  bool
}

func (f *fakeTool) Name() string                            { return f.name }
func (f *fakeTool) Description() string                     { return "fake" }
func (f *fakeTool) InputSchema() json.RawMessage            { return json.RawMessage(`{}`) }
func (f *fakeTool) RequiresPermission(json.RawMessage) bool { return f.needsPerm }
func (f *fakeTool) Execute(context.Context, json.RawMessage) Result {
	f.executed = true
	return Result{Content: "ran"}
}

func TestRegistryCallUnknownTool(t *testing.T) {
	r := NewRegistry(nil)
	result := r.Call(context.Background(), "nope", nil, "")
	if !result.IsError {
		t.Error("expected an error calling an unregistered tool")
	}
}

func TestRegistryCallNoPermissionNeeded(t *testing.T) {
	r := NewRegistry(nil)
	ft := &fakeTool{name: "safe"}
	r.Register(ft)

	result := r.Call(context.Background(), "safe", nil, "")
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if !ft.executed {
		t.Error("expected the tool to have been executed")
	}
}

func TestRegistryCallMissingPermissionHandler(t *testing.T) {
	r := NewRegistry(nil) // no permission func configured
	ft := &fakeTool{name: "dangerous", needsPerm: true}
	r.Register(ft)

	result := r.Call(context.Background(), "dangerous", nil, "")
	if !result.IsError {
		t.Error("expected an error when a permission-requiring tool has no handler")
	}
	if ft.executed {
		t.Error("tool should not have executed without an approval")
	}
}

func TestRegistryCallPermissionDenied(t *testing.T) {
	r := NewRegistry(func(ctx context.Context, toolName, description string) (bool, error) {
		return false, nil
	})
	ft := &fakeTool{name: "dangerous", needsPerm: true}
	r.Register(ft)

	result := r.Call(context.Background(), "dangerous", nil, "")
	if !result.IsError {
		t.Error("expected an error when permission is denied")
	}
	if ft.executed {
		t.Error("tool should not have executed after denial")
	}
}

func TestRegistryCallPermissionApproved(t *testing.T) {
	var gotToolName, gotDescription string
	r := NewRegistry(func(ctx context.Context, toolName, description string) (bool, error) {
		gotToolName, gotDescription = toolName, description
		return true, nil
	})
	ft := &fakeTool{name: "dangerous", needsPerm: true}
	r.Register(ft)

	result := r.Call(context.Background(), "dangerous", json.RawMessage(`{"x":1}`), "custom description")
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if !ft.executed {
		t.Error("expected the tool to have executed after approval")
	}
	if gotToolName != "dangerous" {
		t.Errorf("permission func got tool name %q, want %q", gotToolName, "dangerous")
	}
	if gotDescription != "custom description" {
		t.Errorf("permission func got description %q, want %q", gotDescription, "custom description")
	}
}

func TestRegistryCallPermissionFuncError(t *testing.T) {
	r := NewRegistry(func(ctx context.Context, toolName, description string) (bool, error) {
		return false, errors.New("broker unavailable")
	})
	ft := &fakeTool{name: "dangerous", needsPerm: true}
	r.Register(ft)

	result := r.Call(context.Background(), "dangerous", nil, "")
	if !result.IsError {
		t.Error("expected an error when the permission func itself errors")
	}
}

// fakeSubjectTool additionally implements PermissionSubject, for testing
// that Registry.Call passes the right subject string through to Resolver.
type fakeSubjectTool struct {
	fakeTool
	subject string
}

func (f *fakeSubjectTool) Subject(json.RawMessage) string { return f.subject }

func TestRegistryCallResolverAllowSkipsPermissionFunc(t *testing.T) {
	permCalled := false
	r := NewRegistry(func(context.Context, string, string) (bool, error) {
		permCalled = true
		return true, nil
	})
	r.Resolver = func(toolName, subject string, static bool) Decision { return DecisionAllow }

	ft := &fakeTool{name: "bash", needsPerm: true}
	r.Register(ft)

	result := r.Call(context.Background(), "bash", nil, "")
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if !ft.executed {
		t.Error("expected the tool to execute when Resolver says allow")
	}
	if permCalled {
		t.Error("permission func should not be consulted when Resolver says allow")
	}
}

func TestRegistryCallResolverDenyBlocksWithoutAsking(t *testing.T) {
	permCalled := false
	r := NewRegistry(func(context.Context, string, string) (bool, error) {
		permCalled = true
		return true, nil
	})
	r.Resolver = func(toolName, subject string, static bool) Decision { return DecisionDeny }

	// Even a tool whose static default is "no permission needed" can be
	// blocked outright by the resolver.
	ft := &fakeTool{name: "read_file", needsPerm: false}
	r.Register(ft)

	result := r.Call(context.Background(), "read_file", nil, "")
	if !result.IsError {
		t.Error("expected an error when Resolver says deny")
	}
	if ft.executed {
		t.Error("tool should not execute when Resolver says deny")
	}
	if permCalled {
		t.Error("permission func should not be consulted when Resolver says deny")
	}
}

func TestRegistryCallResolverAskStillGoesThroughPermissionFunc(t *testing.T) {
	r := NewRegistry(func(context.Context, string, string) (bool, error) { return true, nil })
	r.Resolver = func(toolName, subject string, static bool) Decision { return DecisionAsk }

	ft := &fakeTool{name: "safe", needsPerm: false} // static says no permission needed...
	r.Register(ft)

	result := r.Call(context.Background(), "safe", nil, "")
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content) // ...but Resolver overrides to "ask", and the func approves
	}
	if !ft.executed {
		t.Error("expected the tool to execute after the permission func approved")
	}
}

func TestRegistryCallResolverReceivesSubjectFromPermissionSubjectTool(t *testing.T) {
	var gotName, gotSubject string
	var gotStatic bool
	r := NewRegistry(nil)
	r.Resolver = func(toolName, subject string, static bool) Decision {
		gotName, gotSubject, gotStatic = toolName, subject, static
		return DecisionAllow
	}

	ft := &fakeSubjectTool{fakeTool: fakeTool{name: "bash", needsPerm: true}, subject: "git status"}
	r.Register(ft)

	r.Call(context.Background(), "bash", json.RawMessage(`{"command":"git status"}`), "")

	if gotName != "bash" {
		t.Errorf("toolName = %q, want %q", gotName, "bash")
	}
	if gotSubject != "git status" {
		t.Errorf("subject = %q, want %q", gotSubject, "git status")
	}
	if !gotStatic {
		t.Error("staticRequiresPermission should reflect the tool's own RequiresPermission()")
	}
}

func TestRegistryCallResolverSubjectEmptyForNonPermissionSubjectTool(t *testing.T) {
	var gotSubject string
	r := NewRegistry(nil)
	r.Resolver = func(toolName, subject string, static bool) Decision {
		gotSubject = subject
		return DecisionAllow
	}
	r.Register(&fakeTool{name: "plain"})

	r.Call(context.Background(), "plain", nil, "")
	if gotSubject != "" {
		t.Errorf("subject = %q, want empty for a tool that doesn't implement PermissionSubject", gotSubject)
	}
}

func TestRegistryCallPreToolUseHookBlocksBeforePermission(t *testing.T) {
	permCalled := false
	r := NewRegistry(func(context.Context, string, string) (bool, error) {
		permCalled = true
		return true, nil
	})
	r.Hooks = hooks.Config{hooks.EventPreToolUse: {{Command: "echo 'no way' >&2; exit 2"}}}

	ft := &fakeTool{name: "dangerous", needsPerm: true}
	r.Register(ft)

	result := r.Call(context.Background(), "dangerous", nil, "")
	if !result.IsError {
		t.Error("expected an error when pre_tool_use blocks")
	}
	if ft.executed {
		t.Error("tool should not execute when pre_tool_use blocks")
	}
	if permCalled {
		t.Error("permission func should not be consulted once pre_tool_use has already blocked")
	}
}

func TestRegistryCallPreToolUseHookAllowsThenNormalFlowContinues(t *testing.T) {
	r := NewRegistry(nil)
	r.Hooks = hooks.Config{hooks.EventPreToolUse: {{Command: "true"}}}

	ft := &fakeTool{name: "safe", needsPerm: false}
	r.Register(ft)

	result := r.Call(context.Background(), "safe", nil, "")
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if !ft.executed {
		t.Error("expected the tool to execute after pre_tool_use allows")
	}
}

func TestRegistryCallPostToolUseHookRunsAfterExecuteAndCannotUndo(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "post-ran")
	r := NewRegistry(nil)
	// A post_tool_use hook that "blocks" is a documented no-op — the tool
	// has already run by the time it fires.
	r.Hooks = hooks.Config{hooks.EventPostToolUse: {{Command: "echo ran > " + marker + "; exit 2"}}}

	ft := &fakeTool{name: "safe", needsPerm: false}
	r.Register(ft)

	result := r.Call(context.Background(), "safe", nil, "")
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if !ft.executed {
		t.Error("expected the tool to have executed")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("expected the post_tool_use hook to have run")
	}
}

func TestRegistryCallHookReceivesToolNameAndInput(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "captured")
	r := NewRegistry(nil)
	r.Hooks = hooks.Config{hooks.EventPreToolUse: {{Command: "cat > " + out}}}

	ft := &fakeTool{name: "bash", needsPerm: false}
	r.Register(ft)

	r.Call(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`), "")

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read captured payload: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, `"tool_name":"bash"`) {
		t.Errorf("captured payload = %q, want it to contain the tool name", got)
	}
	if !strings.Contains(got, `"command":"ls"`) {
		t.Errorf("captured payload = %q, want it to contain the tool input", got)
	}
}

func TestRegistrySpecsPreservesRegistrationOrder(t *testing.T) {
	r := NewRegistry(nil)
	r.Register(&fakeTool{name: "b"})
	r.Register(&fakeTool{name: "a"})
	r.Register(&fakeTool{name: "c"})

	specs := r.Specs()
	if len(specs) != 3 {
		t.Fatalf("expected 3 specs, got %d", len(specs))
	}
	got := []string{specs[0].Name, specs[1].Name, specs[2].Name}
	want := []string{"b", "a", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("specs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRegistryReRegisterReplacesButKeepsOrder(t *testing.T) {
	r := NewRegistry(nil)
	first := &fakeTool{name: "a"}
	second := &fakeTool{name: "a"} // same name, different instance
	r.Register(first)
	r.Register(second)

	if len(r.Specs()) != 1 {
		t.Fatalf("expected re-registering the same name to not duplicate it, got %d specs", len(r.Specs()))
	}

	r.Call(context.Background(), "a", nil, "")
	if first.executed {
		t.Error("the replaced (first) tool should not have executed")
	}
	if !second.executed {
		t.Error("the replacement (second) tool should have executed")
	}
}

func TestSpecsForRestrictsAndPreservesOrder(t *testing.T) {
	r := NewRegistry(nil)
	r.Register(&fakeTool{name: "read_file"})
	r.Register(&fakeTool{name: "write_file"})
	r.Register(&fakeTool{name: "bash"})

	got := r.SpecsFor([]string{"bash", "read_file"})
	if len(got) != 2 {
		t.Fatalf("expected 2 specs, got %d: %+v", len(got), got)
	}
	// Registration order (read_file, write_file, bash), not allowed-list order.
	if got[0].Name != "read_file" || got[1].Name != "bash" {
		t.Errorf("got %q, %q; want read_file, bash in registration order", got[0].Name, got[1].Name)
	}
}

func TestSpecsForEmptyAllowedMeansUnrestricted(t *testing.T) {
	r := NewRegistry(nil)
	r.Register(&fakeTool{name: "a"})
	r.Register(&fakeTool{name: "b"})

	if got := len(r.SpecsFor(nil)); got != 2 {
		t.Errorf("SpecsFor(nil) = %d specs, want 2 (unrestricted)", got)
	}
	if got := len(r.SpecsFor([]string{})); got != 2 {
		t.Errorf("SpecsFor([]string{}) = %d specs, want 2 (unrestricted)", got)
	}
}

func TestSpecsForUnknownNameSkipped(t *testing.T) {
	r := NewRegistry(nil)
	r.Register(&fakeTool{name: "a"})

	got := r.SpecsFor([]string{"a", "typo_name"})
	if len(got) != 1 || got[0].Name != "a" {
		t.Errorf("expected only the known tool, got %+v", got)
	}
}

func TestIsAllowed(t *testing.T) {
	if !IsAllowed(nil, "anything") {
		t.Error("nil allowed list should permit anything")
	}
	if !IsAllowed([]string{}, "anything") {
		t.Error("empty allowed list should permit anything")
	}
	if !IsAllowed([]string{"read_file", "grep"}, "grep") {
		t.Error("expected grep to be allowed")
	}
	if IsAllowed([]string{"read_file", "grep"}, "bash") {
		t.Error("expected bash to be disallowed")
	}
}
