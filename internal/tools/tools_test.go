package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
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
