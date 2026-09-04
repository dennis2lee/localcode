package debuglog

import "context"

// The sink travels on the context rather than on the Loop.
//
// Two reasons, and the second is the one that decided it. A turn already
// carries its own context everywhere the work goes, including into the
// HTTP request, which is what lets a RoundTripper find the sink without
// every provider learning about this package.
//
// And a prompt that delegates is still one prompt. A sub-agent's turn
// arrives with its parent's context, so its model calls land in the file
// the person's prompt opened, which is what "everything this prompt did"
// has to mean. A sink held per session would have put them in a file of
// their own, or in none.

type sinkKey struct{}

// With puts s on ctx. Callers use Loop-level helpers rather than this
// directly; it is exported because the agent package sets it.
func With(ctx context.Context, s *Sink) context.Context {
	return context.WithValue(ctx, sinkKey{}, s)
}

// From is the sink for work under ctx, or nil when logging is off or
// nothing has opened one.
func From(ctx context.Context) *Sink {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(sinkKey{}).(*Sink)
	return s
}
