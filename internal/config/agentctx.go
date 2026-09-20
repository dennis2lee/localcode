package config

import "context"

// Pinning the running agent name to a unit of work.
//
// Like Smart Agent (see smartctx.go), an agent's identity is pinned to the
// turn context rather than read live. A turn runs under one agent's persona,
// prompt, tool allowlist and permission rules. If default_agent changes or
// permissions are updated mid-turn, the running turn retains its pinned agent
// identity and resolves permissions according to that agent's rules.
type agentKey struct{}

// WithAgent pins the agent name for everything derived from ctx.
func WithAgent(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, agentKey{}, name)
}

// AgentPinned returns the pinned agent name and whether ctx carries one.
func AgentPinned(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	name, ok := ctx.Value(agentKey{}).(string)
	return name, ok
}

// AgentFor returns the agent name to use under ctx: the pinned snapshot
// when one is present, and otherwise c.DefaultAgent (or "" if unset or c is nil).
func (c *Config) AgentFor(ctx context.Context) string {
	if name, ok := AgentPinned(ctx); ok {
		return name
	}
	if c == nil {
		return ""
	}
	return c.DefaultAgent
}
