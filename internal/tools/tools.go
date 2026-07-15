// Package tools implements the small set of built-in tools (file I/O, shell,
// search) the agent loop exposes to the model, plus a permission gate for
// side-effecting ones.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"localcode/internal/provider"
)

// Result is what a tool execution produces; Content goes back to the model
// as a tool_result block.
type Result struct {
	Content string
	IsError bool
}

// Tool is one callable capability exposed to the model.
type Tool interface {
	Name() string
	Description() string
	InputSchema() json.RawMessage
	// RequiresPermission reports whether this call needs interactive
	// approval before running (e.g. writing a file, running a shell
	// command). input is provided so permission text can describe exactly
	// what's about to happen.
	RequiresPermission(input json.RawMessage) bool
	Execute(ctx context.Context, input json.RawMessage) Result
}

// PermissionFunc is asked to approve a side-effecting tool call before it
// runs. description is human-readable ("run: rm -rf build/").
type PermissionFunc func(ctx context.Context, toolName, description string) (bool, error)

// Registry holds the tools available to an agent loop and mediates
// permission checks around execution.
type Registry struct {
	tools      map[string]Tool
	order      []string
	permission PermissionFunc
}

func NewRegistry(permission PermissionFunc) *Registry {
	return &Registry{tools: map[string]Tool{}, permission: permission}
}

func (r *Registry) Register(t Tool) {
	if _, exists := r.tools[t.Name()]; !exists {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}

// Specs returns provider-facing tool specs in registration order, for
// inclusion in a ChatRequest.
func (r *Registry) Specs() []provider.Tool {
	out := make([]provider.Tool, 0, len(r.order))
	for _, name := range r.order {
		t := r.tools[name]
		out = append(out, provider.Tool{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	return out
}

// Call runs a tool by name, applying the permission gate first if the tool
// requires it. describe, if non-empty, overrides the default permission
// prompt text.
func (r *Registry) Call(ctx context.Context, name string, input json.RawMessage, describe string) Result {
	t, ok := r.tools[name]
	if !ok {
		return Result{Content: fmt.Sprintf("unknown tool %q", name), IsError: true}
	}

	if t.RequiresPermission(input) {
		if r.permission == nil {
			return Result{Content: fmt.Sprintf("tool %q requires permission but no permission handler is configured", name), IsError: true}
		}
		if describe == "" {
			describe = fmt.Sprintf("%s %s", name, string(input))
		}
		ok, err := r.permission(ctx, name, describe)
		if err != nil {
			return Result{Content: fmt.Sprintf("permission check failed: %v", err), IsError: true}
		}
		if !ok {
			return Result{Content: "denied by user", IsError: true}
		}
	}

	return t.Execute(ctx, input)
}

func schema(properties string, required ...string) json.RawMessage {
	req, _ := json.Marshal(required)
	return json.RawMessage(fmt.Sprintf(`{"type":"object","properties":%s,"required":%s}`, properties, req))
}
