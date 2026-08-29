// Package tools implements the small set of built-in tools (file I/O, shell,
// search) the agent loop exposes to the model, plus a permission gate for
// side-effecting ones.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"localcode/internal/hooks"
	"localcode/internal/provider"
	"sync"
)

// Result is what a tool execution produces; Content goes back to the model
// as a tool_result block.
type Result struct {
	Content string
	IsError bool
	// Refused marks a call that did not run because someone said no — a
	// deny rule, a blocking hook, or the person at the keyboard clicking
	// Deny. It is a different kind of failure from a command that ran and
	// failed, and the difference matters to anything deciding whether to
	// carry on: after a refusal, a model that stops has stopped for the
	// right reason.
	Refused bool
	// Sources names the material inside Content that came from
	// somewhere other than this tool, with the span of Content each one
	// occupies.
	//
	// It exists because "who wrote this result" cannot be answered from
	// the tool's name. Two of the delegation tools return a sub-agent's
	// own words and one returns a sentence localcode wrote saying the
	// work has started; classifying by tool name made that
	// acknowledgement a report from a child that had not said anything
	// yet. A tool that carries somebody else's material says so here,
	// and a tool that does not say so is answering for itself.
	//
	// Spans rather than copies, so a collection of four answers is
	// described by four spans of one string instead of four copies of
	// all four. That is also what makes a per-child hash cover that
	// child's words and nothing else.
	//
	// Free-form identities, interpreted by whoever recorded them. The
	// tools package does not know what a sub-agent is and does not need
	// to.
	Sources []ResultSource
}

// ResultSource is one contributor's material inside a Result's Content:
// who it came from, and where in Content it is. From and To are byte
// offsets, half-open, as a slice expression takes them.
type ResultSource struct {
	ID   string
	From int
	To   int
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

// Contextual is implemented by a tool whose description or input schema
// depends on the turn it is being offered to, rather than being the same
// for the life of the process.
//
// The delegation tools are the case this exists for. Their schema carries
// an enum of the agents that may be delegated to, and that roster depends
// on whether Smart Agent was on when the turn was admitted. Rendering it
// from the live setting instead let a turn advertise one roster to the
// model and accept a different one at execution: the switch flipped
// mid-turn, the next round's schema lost six names the running turn would
// still have honoured, and the tool-schema half of the cached prefix
// changed underneath a turn that was marking it as stable.
//
// A tool that does not implement this is asked through Description and
// InputSchema as before, which is nearly all of them.
type Contextual interface {
	DescriptionFor(ctx context.Context) string
	InputSchemaFor(ctx context.Context) json.RawMessage
}

// PermissionFunc is asked to approve a side-effecting tool call before it
// runs.
type PermissionFunc func(ctx context.Context, ask Ask) (bool, error)

// RefusedError is a refusal that carries its own explanation.
//
// An ordinary false from a PermissionFunc means somebody said no, and
// "denied by user" is the right thing to tell the model. A turn nobody is
// watching can also be refused because nobody answered, and reporting
// that as a denial would tell the model something untrue about what
// happened. So the reason travels with the refusal.
type RefusedError struct{ Reason string }

func (e *RefusedError) Error() string { return e.Reason }

// Ask is one question put to the person at the keyboard.
type Ask struct {
	Tool string
	// Subject is the same pattern-matchable string PermissionSubject
	// exposes ("" if the tool has none), so an "always allow" decision
	// knows what pattern it is actually granting.
	Subject string
	// Description is human-readable ("run: rm -rf build/").
	Description string
	// Outside, Dir and Workspace are set only when the workspace boundary
	// is what raised this question, and they are what lets the prompt say
	// so. A question that cannot say why it is being asked is one people
	// answer without reading: "write_file /Users/me/other/x.go" looks
	// like every other write until something points out that it is not in
	// this project.
	//
	// Dir is what an "allow this directory" answer covers. See OutsideDir.
	Outside   OutsideClass
	Dir       string
	Workspace string
}

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
//
// ctx is passed because a decision can depend on where the call is
// happening as well as what it is: the workspace this turn belongs to
// rides on the context (see workdir.go), and "is this path outside the
// project?" is not answerable without it.
type PermissionResolver func(ctx context.Context, q Query) Outcome

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
	// mu guards tools and order. Registration used to happen only at
	// startup, before anything could race it; "/reset-mcp" and
	// "/reset-skills" now swap tools while turns are running, and a
	// turn's SpecsFor iterating order during a swap is exactly the race
	// the map would lose.
	mu         sync.RWMutex
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
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[t.Name()]; !exists {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}

// Deregister removes a tool by name. A no-op for a name that is not
// registered, so a reload can hand it yesterday's list without checking.
//
// It exists for "/reset-mcp": a server removed from the configuration
// has to take its tools with it, or the model goes on being offered
// calls that can only fail.
func (r *Registry) Deregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[name]; !exists {
		return
	}
	delete(r.tools, name)
	for i, n := range r.order {
		if n == name {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
}

// Specs returns provider-facing tool specs in registration order, for
// inclusion in a ChatRequest.
func (r *Registry) Specs(ctx context.Context) []provider.Tool {
	return r.SpecsFor(ctx, nil)
}

// SpecsFor is like Specs but restricted to the named tools, preserving
// registration order. A nil/empty allowed list means no restriction (same
// as Specs). Unknown names are silently skipped — an agent config
// referencing a typo'd tool name just gets fewer tools, not a crash.
// ctx is the turn's, so a Contextual tool renders the same schema for
// every round of one turn.
// NamesFor is the names SpecsFor would advertise, in the same order.
//
// It exists so the prompt assembly can be told which tools the model
// will actually be offered without building the schemas twice. A nil
// allowlist means everything, which is why this cannot be answered by
// looking at the allowlist alone: nil reads as zero tools, and the
// request has all of them.
func (r *Registry) NamesFor(ctx context.Context, allowed []string) []string {
	specs := r.SpecsFor(ctx, allowed)
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Name
	}
	return out
}

func (r *Registry) SpecsFor(ctx context.Context, allowed []string) []provider.Tool {
	allowSet := toSet(allowed)
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]provider.Tool, 0, len(r.order))
	for _, name := range r.order {
		if allowSet != nil && !allowSet[name] {
			continue
		}
		t := r.tools[name]
		spec := provider.Tool{Name: t.Name()}
		if c, ok := t.(Contextual); ok {
			spec.Description = c.DescriptionFor(ctx)
			spec.InputSchema = c.InputSchemaFor(ctx)
		} else {
			spec.Description = t.Description()
			spec.InputSchema = t.InputSchema()
		}
		out = append(out, spec)
	}
	return out
}

// Names lists every registered tool, in registration order.
//
// Used to turn "everything except these" into a concrete allowlist, which
// is the only shape the rest of the code understands: an agent's Tools
// field, the specs the model is shown, and the check in runTools are all
// allowlists, and a single subtractive case is not worth a second
// mechanism running beside them.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.order...)
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
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return Result{Content: fmt.Sprintf("unknown tool %q", name), IsError: true}
	}

	if len(r.Hooks) > 0 {
		// The same directory the tool itself is about to resolve paths
		// against: a hook that checks or formats what a tool is doing has
		// to be looking at the same project the tool is.
		blocked, reason, _ := hooks.Run(ctx, r.Hooks, hooks.EventPreToolUse, WorkingDir(ctx), map[string]any{
			"tool_name":  name,
			"tool_input": json.RawMessage(input),
		})
		if blocked {
			return Result{Content: fmt.Sprintf("blocked by pre_tool_use hook: %s", reason), IsError: true, Refused: true}
		}
	}

	subject := ""
	if ps, ok := t.(PermissionSubject); ok {
		subject = ps.Subject(input)
	}

	outcome := Outcome{Decision: DecisionAsk}
	if !t.RequiresPermission(input) {
		outcome.Decision = DecisionAllow
	}
	if r.Resolver != nil {
		outcome = r.Resolver(ctx, Query{
			Tool:    name,
			Subject: subject,
			Static:  t.RequiresPermission(input),
			// Asked of the tool, so a tool that touches paths cannot be
			// left out of the boundary by forgetting to list it here.
			Class: ClassOf(t),
		})
	}

	switch outcome.Decision {
	case DecisionDeny:
		return Result{Content: fmt.Sprintf("tool %q is denied by permission policy", name), IsError: true, Refused: true}

	case DecisionAsk:
		if r.permission == nil {
			return Result{Content: fmt.Sprintf("tool %q requires permission but no permission handler is configured", name), IsError: true}
		}
		if describe == "" {
			describe = fmt.Sprintf("%s %s", name, string(input))
		}
		allowed, err := r.permission(ctx, Ask{
			Tool:        name,
			Subject:     subject,
			Description: describe,
			Outside:     outcome.Outside,
			Dir:         outcome.Dir,
			Workspace:   outcome.Workspace,
		})
		var refused *RefusedError
		if errors.As(err, &refused) {
			// A refusal that knows why it happened. Marked Refused like
			// any other, so the carry-on nudge treats it as a stop
			// rather than as work still to do.
			return Result{Content: refused.Reason, IsError: true, Refused: true}
		}
		if err != nil {
			return Result{Content: fmt.Sprintf("permission check failed: %v", err), IsError: true}
		}
		if !allowed {
			return Result{Content: "denied by user", IsError: true, Refused: true}
		}
	}

	result := t.Execute(ctx, input)

	if len(r.Hooks) > 0 {
		// post_tool_use is fire-and-forget: the tool has already run, so
		// there's nothing left to block — a "block" decision here is
		// simply ignored. This is the hook point for side effects like
		// auto-formatting a file right after an edit.
		hooks.Run(ctx, r.Hooks, hooks.EventPostToolUse, WorkingDir(ctx), map[string]any{
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
