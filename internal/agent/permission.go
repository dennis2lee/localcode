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

		// Where this question has to appear for someone to answer it. For
		// an ordinary session that is the session itself; for a background
		// task it is also the conversation that spawned it, since nothing
		// streams a task's own log.
		ask := b.audience(sessionID)

		for _, target := range ask.mirrors {
			b.store.Append(target, events.TypePermissionRequest, map[string]any{
				"id":   id,
				"tool": toolName,
				// Prefixed rather than carried in a separate field so both
				// clients say where it came from without needing to know
				// about tasks: the request is for work the user started
				// but is not watching, and answering it as if it came from
				// the conversation on screen would be misleading.
				"description":  fmt.Sprintf("[background task %s] %s", sessionID, description),
				"rule":         rule.Match,
				"can_always":   b.ConfigPath != "",
				"task_session": sessionID,
			})
		}

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
			for _, target := range ask.all() {
				b.store.Append(target, events.TypePermissionResolved, map[string]any{
					"id": id, "allow": res.allow, "scope": res.scope,
				})
			}
			return res.allow, nil
		case <-ctx.Done():
			b.mu.Lock()
			delete(b.pending, id)
			b.mu.Unlock()
			// The question is gone, so the modal asking it has to go too —
			// otherwise cancelling a stuck task leaves its request on
			// screen in the parent conversation, refusing every message
			// for a task that no longer exists.
			for _, target := range ask.all() {
				b.store.Append(target, events.TypePermissionResolved, map[string]any{
					"id": id, "allow": false, "scope": ScopeOnce, "cancelled": true,
				})
			}
			return false, ctx.Err()
		}
	}
}

// audience is where one permission request has to be written: the
// session that raised it, plus any session a person is actually looking
// at.
type audience struct {
	origin  string
	mirrors []string
}

func (a audience) all() []string { return append([]string{a.origin}, a.mirrors...) }

// maxParentWalk bounds the climb from a task to the conversation that
// started it. Task delegation is capped at three levels, so this is
// already generous; it exists so a corrupted parent chain cannot loop
// forever inside a tool call.
const maxParentWalk = 8

// audience finds the user-facing session above sessionID, if it is not
// one itself.
//
// A background task is a session with no client attached to it: the
// parent's log gets task.spawned and task.status, and nothing else. A
// permission request raised inside one was therefore written somewhere
// nobody reads, and the tool call blocked on an answer that could not
// arrive — indefinitely, since a request has no timeout. The task held
// one of the TaskManager's concurrency slots the whole time, and the only
// visible symptom was "1 background task" that never finished.
//
// The ids the broker hands out are process-global, so a mirrored request
// is answerable from wherever it is shown, with no routing: the resolve
// endpoint looks the id up in one table.
func (b *PermissionBroker) audience(sessionID string) audience {
	a := audience{origin: sessionID}
	id := sessionID
	for range maxParentWalk {
		sess, err := b.store.Get(id)
		if err != nil || sess.ParentID == "" {
			break
		}
		id = sess.ParentID
		if id == sessionID {
			break // a cycle; the origin already has it
		}
		// Only sessions someone can open. An intermediate task in a chain
		// of delegations is as unwatched as the one that asked, so writing
		// the request there would add log noise and no answer.
		if parent, err := b.store.Get(id); err == nil && parent.Visible {
			a.mirrors = append(a.mirrors, id)
		}
	}
	return a
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
// Resolve answers a pending request and reports whether there was one.
//
// The answer matters to the caller: an id that is unknown or already
// answered used to be reported as a success, so a client resolving a
// stale prompt — a replayed one, or a second client a moment late — was
// told it worked while nothing happened.
func (b *PermissionBroker) Resolve(id string, allow bool, scope string) bool {
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
	return ok
}
