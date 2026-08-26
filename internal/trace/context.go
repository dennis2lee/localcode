package trace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

// The trace id, and how it reaches a sub-agent.
//
// One id per top-level turn, carried on the context. Everything a turn
// does inherits it: a tool call, a synchronous delegation (whose whole
// child session runs under the caller's context), and a background one
// (which is handed the id explicitly, because it deliberately outlives the
// context it was launched from).
//
// This is the piece that makes the log a story rather than a pile. Without
// it a delegation to three specialists is four unrelated sequences of
// lines in one file, interleaved with whatever else the daemon was doing.

type idKey struct{}

// WithID returns ctx carrying id. An empty id is a no-op, so a caller with
// nothing to say does not have to special-case it.
func WithID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, idKey{}, id)
}

// ID reports the trace id on ctx, or "" when there is none.
func ID(ctx context.Context) string {
	id, _ := ctx.Value(idKey{}).(string)
	return id
}

// NewID mints one. Eight bytes: long enough that two turns on one daemon
// will not collide, short enough to read off a terminal and paste into a
// grep, which is the only thing anyone does with it.
func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail in practice, and a trace id is not
		// worth failing a turn over if it somehow does. An empty id
		// disables correlation for that turn and nothing else.
		return ""
	}
	return hex.EncodeToString(b[:])
}
