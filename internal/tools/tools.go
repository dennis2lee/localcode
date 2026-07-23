// Package tools implements the small set of built-in tools (file I/O, shell,
// search) the agent loop exposes to the model, plus a permission gate for
// side-effecting ones.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"localcode/internal/hooks"
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
// runs. subject is the same pattern-matchable string PermissionSubject
// exposes ("" if the tool has none) — passed through so an "always allow"
// decision knows what pattern it's actually granting. description is
// human-readable ("run: rm -rf build/").
type PermissionFunc func(ctx context.Context, toolName, subject, description string) (bool, error)

// Decision is a resolved permission outcome for one tool call — mirrors
// config.Decision (same underlying string values: "allow"/"ask"/"deny")
// without this package importing internal/config, so the two stay
// decoupled; Loop is what bridges them via Resolver.
type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionAsk   Decision = "ask"
	DecisionDeny  Decision = "deny"
)

// PermissionResolver decides allow/ask/deny for a call to toolName given
// subject (see PermissionSubject) and the tool's own static default
// (staticRequiresPermission, from Tool.RequiresPermission). A Registry
// with no Resolver set falls back to exactly that static default (ask iff
// RequiresPermission, else allow) — today's pre-permission-config
// behavior.
type PermissionResolver func(toolName, subject string, staticRequiresPermission bool) Decision

// PermissionSubject is implemented by tools whose input has a natural
// pattern-matchable "subject" — a shell command for Bash, a file path for
// WriteFile/Edit — so permission rules can match against it (e.g. allow
// "git *" but ask for everything else). Tools that don't implement it
// only match a rule's "*" pattern.
type PermissionSubject interface {
	Subject(input json.RawMessage) string
}

// Registry holds the tools available to an agent loop and mediates
// permission checks around execution.
type Registry struct {
	tools      map[string]Tool
	order      []string
	permission PermissionFunc

	// Resolver, if set, is consulted before the static
	// Tool.RequiresPermission check — see PermissionResolver.
	Resolver PermissionResolver

	// Hooks, if set, runs pre_tool_use (can block the call outright,
	// before permission is even considered) and post_tool_use (fire-and-
	// forget, e.g. auto-formatting a file after an edit — its block
	// decision is a no-op since the tool has already run) around Call.
	Hooks hooks.Config
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
	return r.SpecsFor(nil)
}

// SpecsFor is like Specs but restricted to the named tools, preserving
// registration order. A nil/empty allowed list means no restriction (same
// as Specs). Unknown names are silently skipped — an agent config
// referencing a typo'd tool name just gets fewer tools, not a crash.
func (r *Registry) SpecsFor(allowed []string) []provider.Tool {
	allowSet := toSet(allowed)
	out := make([]provider.Tool, 0, len(r.order))
	for _, name := range r.order {
		if allowSet != nil && !allowSet[name] {
			continue
		}
		t := r.tools[name]
		out = append(out, provider.Tool{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	return out
}

// IsAllowed reports whether name is permitted under an allowed list from
// an agent's Tools restriction. A nil/empty list means unrestricted.
func IsAllowed(allowed []string, name string) bool {
	if len(allowed) == 0 {
		return true
	}
	return toSet(allowed)[name]
}

func toSet(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

// Call runs a tool by name, first resolving allow/ask/deny (via Resolver
// if set, else the tool's own static RequiresPermission default) and
// gating on the permission broker if the resolution is "ask". describe, if
// non-empty, overrides the default permission prompt text.
func (r *Registry) Call(ctx context.Context, name string, input json.RawMessage, describe string) Result {
	t, ok := r.tools[name]
	if !ok {
		return Result{Content: fmt.Sprintf("unknown tool %q", name), IsError: true}
	}

	if len(r.Hooks) > 0 {
		blocked, reason, _ := hooks.Run(ctx, r.Hooks, hooks.EventPreToolUse, map[string]any{
			"tool_name":  name,
			"tool_input": json.RawMessage(input),
		})
		if blocked {
			return Result{Content: fmt.Sprintf("blocked by pre_tool_use hook: %s", reason), IsError: true}
		}
	}

	subject := ""
	if ps, ok := t.(PermissionSubject); ok {
		subject = ps.Subject(input)
	}

	decision := DecisionAsk
	if !t.RequiresPermission(input) {
		decision = DecisionAllow
	}
	if r.Resolver != nil {
		decision = r.Resolver(name, subject, t.RequiresPermission(input))
	}

	switch decision {
	case DecisionDeny:
		return Result{Content: fmt.Sprintf("tool %q is denied by permission policy", name), IsError: true}

	case DecisionAsk:
		if r.permission == nil {
			return Result{Content: fmt.Sprintf("tool %q requires permission but no permission handler is configured", name), IsError: true}
		}
		if describe == "" {
			describe = fmt.Sprintf("%s %s", name, string(input))
		}
		allowed, err := r.permission(ctx, name, subject, describe)
		if err != nil {
			return Result{Content: fmt.Sprintf("permission check failed: %v", err), IsError: true}
		}
		if !allowed {
			return Result{Content: "denied by user", IsError: true}
		}
	}

	result := t.Execute(ctx, input)

	if len(r.Hooks) > 0 {
		// post_tool_use is fire-and-forget: the tool has already run, so
		// there's nothing left to block — a "block" decision here is
		// simply ignored. This is the hook point for side effects like
		// auto-formatting a file right after an edit.
		hooks.Run(ctx, r.Hooks, hooks.EventPostToolUse, map[string]any{
			"tool_name":   name,
			"tool_input":  json.RawMessage(input),
			"tool_output": result.Content,
			"is_error":    result.IsError,
		})
	}

	return result
}

func schema(properties string, required ...string) json.RawMessage {
	req, _ := json.Marshal(required)
	return json.RawMessage(fmt.Sprintf(`{"type":"object","properties":%s,"required":%s}`, properties, req))
}
