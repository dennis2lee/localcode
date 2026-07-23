package agent

import (
	"context"
	"fmt"
	"log"
	"sync"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/session"
	"localcode/internal/tools"
)

type ctxKey int

const sessionIDKey ctxKey = 0

func WithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionIDKey, id)
}

func SessionIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(sessionIDKey).(string)
	return v, ok
}

// Permission scopes an approval can carry, answering "and don't ask me
// again for..." with three different amounts of again.
const (
	// ScopeOnce approves exactly this call. The default, and what a bare
	// allow with no scope means.
	ScopeOnce = "once"
	// ScopeSession approves matching calls for the rest of this session,
	// in memory only. Forgotten on daemon restart.
	ScopeSession = "session"
	// ScopeAlways approves matching calls permanently by writing a rule
	// into config.json, and also covers the current session.
	ScopeAlways = "always"
)

// PermissionBroker turns a blocking tool-permission check into a
// request/response pair of session events (permission.request /
// permission.resolved), so any subscribed client (TUI, web) can answer it.
// The tool call goroutine blocks on Func's returned call until Resolve is
// invoked with the matching id, or the context is cancelled.
type PermissionBroker struct {
	store *session.Store

	// ConfigPath is where ScopeAlways writes its rule. Empty disables
	// permanent approvals, leaving only once and session, which is the
	// right behavior when the daemon was started against a config file it
	// has no business rewriting.
	ConfigPath string

	mu      sync.Mutex
	counter int
	pending map[string]chan resolution
	// granted remembers ScopeSession (and ScopeAlways) approvals, keyed by
	// session, then by the rule pattern that was approved. Keeping it here
	// rather than in the config Resolver is deliberate: these grants are
	// per session and must not leak into other sessions or outlive the
	// process.
	granted map[string]map[string]bool
}

// resolution is one answer to a permission request: whether to allow, and
// how long that answer lasts.
type resolution struct {
	allow bool
	scope string
}

func NewPermissionBroker(store *session.Store) *PermissionBroker {
	return &PermissionBroker{
		store:   store,
		pending: map[string]chan resolution{},
		granted: map[string]map[string]bool{},
	}
}

func (b *PermissionBroker) Func() tools.PermissionFunc {
	return func(ctx context.Context, toolName, subject, description string) (bool, error) {
		sessionID, ok := SessionIDFromContext(ctx)
		if !ok {
			return false, fmt.Errorf("permission check has no session context")
		}

		// An earlier "allow for this session" (or "always") already covers
		// this call, so don't ask again.
		rule := config.PermissionRuleFor(toolName, subject)
		if b.isGranted(sessionID, toolName, rule.Match) {
			return true, nil
		}

		b.mu.Lock()
		b.counter++
		id := fmt.Sprintf("p%d", b.counter)
		ch := make(chan resolution, 1)
		b.pending[id] = ch
		b.mu.Unlock()

		if _, err := b.store.Append(sessionID, events.TypePermissionRequest, map[string]any{
			"id":          id,
			"tool":        toolName,
			"description": description,
			// The pattern a session/always approval would grant, so clients
			// can show what is actually being widened rather than making
			// the user guess.
			"rule":       rule.Match,
			"can_always": b.ConfigPath != "",
		}); err != nil {
			return false, err
		}

		select {
		case res := <-ch:
			if res.allow && res.scope != ScopeOnce {
				b.grant(sessionID, toolName, rule.Match)
			}
			if res.allow && res.scope == ScopeAlways {
				if err := b.persist(toolName, rule); err != nil {
					// The approval still stands for this session; only the
					// permanence failed, which is worth logging but not
					// worth refusing the tool call the user just approved.
					log.Printf("permission: could not persist %q rule %q: %v", toolName, rule.Match, err)
				}
			}
			b.store.Append(sessionID, events.TypePermissionResolved, map[string]any{
				"id": id, "allow": res.allow, "scope": res.scope,
			})
			return res.allow, nil
		case <-ctx.Done():
			b.mu.Lock()
			delete(b.pending, id)
			b.mu.Unlock()
			return false, ctx.Err()
		}
	}
}

// grantKey namespaces a remembered approval by tool, so allowing the bash
// pattern "npm *" never also allows a write_file path that happens to
// glob the same way.
func grantKey(toolName, pattern string) string { return toolName + "\x00" + pattern }

func (b *PermissionBroker) grant(sessionID, toolName, pattern string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.granted[sessionID] == nil {
		b.granted[sessionID] = map[string]bool{}
	}
	b.granted[sessionID][grantKey(toolName, pattern)] = true
}

func (b *PermissionBroker) isGranted(sessionID, toolName, pattern string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.granted[sessionID][grantKey(toolName, pattern)]
}

func (b *PermissionBroker) persist(toolName string, rule config.PermissionRule) error {
	if b.ConfigPath == "" {
		return fmt.Errorf("no config path configured for permanent permissions")
	}
	return config.AddPermissionRuleToFile(b.ConfigPath, toolName, rule)
}

// ForgetSession drops a session's remembered approvals, so a deleted
// session doesn't keep granting permissions to an id that could later be
// reused, and so the map doesn't grow forever.
func (b *PermissionBroker) ForgetSession(sessionID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.granted, sessionID)
}

// Resolve answers a pending permission request. It is a no-op if id is
// unknown (already resolved or timed out). scope is one of ScopeOnce,
// ScopeSession, or ScopeAlways; anything else is treated as ScopeOnce, so
// an older client that only sends {"allow":true} keeps working unchanged.
func (b *PermissionBroker) Resolve(id string, allow bool, scope string) {
	switch scope {
	case ScopeSession, ScopeAlways:
	default:
		scope = ScopeOnce
	}

	b.mu.Lock()
	ch, ok := b.pending[id]
	if ok {
		delete(b.pending, id)
	}
	b.mu.Unlock()
	if ok {
		ch <- resolution{allow: allow, scope: scope}
	}
}
