package config

import "context"

// Pinning the orchestration switch to a unit of work, for the reason the
// Smart Agent one is pinned: see smartctx.go. A run resolves its roster,
// its stage tool allowlists and its refusals from one answer, and a switch
// flipped while a thirty-minute run is in a fanout must not reach it.
type orchestrateKey struct{}

// WithOrchestrate pins on for everything derived from ctx.
func WithOrchestrate(ctx context.Context, on bool) context.Context {
	return context.WithValue(ctx, orchestrateKey{}, on)
}

// OrchestratePinned returns the pinned value and whether ctx carries one.
func OrchestratePinned(ctx context.Context) (bool, bool) {
	if ctx == nil {
		return false, false
	}
	on, ok := ctx.Value(orchestrateKey{}).(bool)
	return on, ok
}

// OrchestrateFor is the setting work under ctx should use: the pinned
// snapshot when there is one, and otherwise the live value.
//
// A free function taking the config rather than a method on it, because the
// caller that most needs it is a tool, and a tool holding a *Config it
// might read while another goroutine writes is the shape this package
// keeps behind permMu. cfg may be nil, which reads as off.
func OrchestrateFor(ctx context.Context, cfg *Config) bool {
	if on, ok := OrchestratePinned(ctx); ok {
		return on
	}
	return cfg.OrchestrateLive()
}
