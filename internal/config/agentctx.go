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

// AgentFor returns the agent whose rules apply under ctx: the one pinned
// to this unit of work, and "" when none is.
//
// Empty rather than default_agent, and the difference is a permission
// bypass. A decision asked for outside a turn — a hook, a command, a
// health check, anything that never set the pin — is not that agent's
// work, and answering it with that agent's rules applies an override
// somebody wrote for one persona to everything that has no persona at
// all. It cut both ways: a default_agent allowing bash made a pin-less
// check allow it over a global deny, and one denying bash refused work
// the global rules permitted.
//
// "" means the top-level rules alone, which is what the program did
// before per-agent rules existed and what a caller with no agent is
// asking about.
func (c *Config) AgentFor(ctx context.Context) string {
	name, _ := AgentPinned(ctx)
	return name
}
