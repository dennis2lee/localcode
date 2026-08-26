package config

import "context"

// Pinning the Smart Agent setting to a unit of work.
//
// The switch is live: it can be flipped from the settings panel or from
// "/config smart_agent on" while turns are running. That is the feature.
// The problem is that "live" and "read again on every use" are not the
// same thing, and the second one is wrong.
//
// Smart Agent decides which agents exist, which tools they may call, which
// model answers, where a fallback goes, whether the prompt prefix is
// marked for caching, whether the credential guard applies, and whether
// the turn is written to the trace. Reading the switch separately at each
// of those points lets one unit of work run half enabled: a turn whose
// tool allowlist was chosen while it was on, calling tools whose guards
// are consulted after it went off, or a background task admitted as a
// read-only specialist that starts, minutes later, with no restriction at
// all because the roster it was resolved from no longer exists.
//
// So the value is read once, at the point work is admitted, and pinned to
// that work's context. Everything derived from that context sees the same
// answer until it ends. Flipping the switch still takes effect on the next
// turn and the next delegation, which is what "live" was ever promising.
type smartAgentKey struct{}

// WithSmartAgent pins on for everything derived from ctx.
func WithSmartAgent(ctx context.Context, on bool) context.Context {
	return context.WithValue(ctx, smartAgentKey{}, on)
}

// SmartAgentPinned returns the pinned value and whether ctx carries one.
func SmartAgentPinned(ctx context.Context) (bool, bool) {
	if ctx == nil {
		return false, false
	}
	on, ok := ctx.Value(smartAgentKey{}).(bool)
	return on, ok
}

// SmartAgentFor is the setting work under ctx should use: the pinned
// snapshot when there is one, and otherwise the live value.
//
// The fallback matters as much as the pin. Plenty of reads have no unit of
// work behind them — the settings panel asking what the switch says, a
// tool description being rendered for a listing — and those want the
// current answer, not a stale one.
// A nil Config reads as off, so a component assembled without one — which
// is a real shape in tests and in the embedded loop — is not a panic at a
// point that only wanted to know whether an optional feature is on.
func (c *Config) SmartAgentFor(ctx context.Context) bool {
	if on, ok := SmartAgentPinned(ctx); ok {
		return on
	}
	if c == nil {
		return false
	}
	return c.SmartAgentLive()
}
