package agent

import (
	"context"
	"fmt"
	"sync"

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

// PermissionBroker turns a blocking tool-permission check into a
// request/response pair of session events (permission.request /
// permission.resolved), so any subscribed client (TUI, web) can answer it.
// The tool call goroutine blocks on Func's returned call until Resolve is
// invoked with the matching id, or the context is cancelled.
type PermissionBroker struct {
	store *session.Store

	mu      sync.Mutex
	counter int
	pending map[string]chan bool
}

func NewPermissionBroker(store *session.Store) *PermissionBroker {
	return &PermissionBroker{store: store, pending: map[string]chan bool{}}
}

func (b *PermissionBroker) Func() tools.PermissionFunc {
	return func(ctx context.Context, toolName, description string) (bool, error) {
		sessionID, ok := SessionIDFromContext(ctx)
		if !ok {
			return false, fmt.Errorf("permission check has no session context")
		}

		b.mu.Lock()
		b.counter++
		id := fmt.Sprintf("p%d", b.counter)
		ch := make(chan bool, 1)
		b.pending[id] = ch
		b.mu.Unlock()

		if _, err := b.store.Append(sessionID, events.TypePermissionRequest, map[string]any{
			"id":          id,
			"tool":        toolName,
			"description": description,
		}); err != nil {
			return false, err
		}

		select {
		case allow := <-ch:
			b.store.Append(sessionID, events.TypePermissionResolved, map[string]any{"id": id, "allow": allow})
			return allow, nil
		case <-ctx.Done():
			b.mu.Lock()
			delete(b.pending, id)
			b.mu.Unlock()
			return false, ctx.Err()
		}
	}
}

// Resolve answers a pending permission request. It is a no-op if id is
// unknown (already resolved or timed out).
func (b *PermissionBroker) Resolve(id string, allow bool) {
	b.mu.Lock()
	ch, ok := b.pending[id]
	if ok {
		delete(b.pending, id)
	}
	b.mu.Unlock()
	if ok {
		ch <- allow
	}
}
