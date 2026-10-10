package agent

import (
	"context"
	"fmt"
	"strings"

	"localcode/internal/config"
)

// The agents a piece of delegated work has passed through, root first.
//
// The depth limit bounds how far delegation goes, and nothing bounded where
// it went. A sub-agent offered the whole roster was offered itself, and a
// model that could not do what it was asked handed the same request to the
// same agent: a "vision" agent given a path to an image, with no tool that
// could open one, delegated to "vision", which delegated to "vision", until
// the depth limit stopped it three levels down. Every level spent its own
// turns finding out what the first one had.
//
// So a delegated turn may not hand its work to an agent already working on
// it, itself included, and is not offered one. The turn a person is having
// is not a delegation and keeps its whole roster: a top-level agent handing
// a question to a fresh copy of itself is a context it does not have to
// pay for, not a loop.
type agentChainKey struct{}

// withAgentInChain records that agent is now working on the turn ctx
// belongs to. The slice is copied, so two children of one parent never
// share a backing array.
func withAgentInChain(ctx context.Context, agent string) context.Context {
	prev := agentChain(ctx)
	chain := make([]string, len(prev), len(prev)+1)
	copy(chain, prev)
	return context.WithValue(ctx, agentChainKey{}, append(chain, agent))
}

func agentChain(ctx context.Context) []string {
	chain, _ := ctx.Value(agentChainKey{}).([]string)
	return chain
}

// withChainFrom carries from's chain onto ctx, for a background task: its
// context is built fresh from the manager's own, and would otherwise start
// with no record of who launched it.
func withChainFrom(ctx, from context.Context) context.Context {
	if chain := agentChain(from); len(chain) > 0 {
		return context.WithValue(ctx, agentChainKey{}, chain)
	}
	return ctx
}

// delegationRefusal says why this turn may not hand its work to target,
// or "" when it may.
func delegationRefusal(ctx context.Context, target string) string {
	if taskDepthFromContext(ctx) == 0 {
		return ""
	}
	chain := agentChain(ctx)
	for i, a := range chain {
		if a != target {
			continue
		}
		path := strings.Join(chain, " -> ")
		if i == len(chain)-1 {
			return fmt.Sprintf("you are the %q agent, and this task was delegated to you (%s); "+
				"handing it to %q again would only repeat this turn. Do it with the tools you have, "+
				"or say in your answer what you could not do and why.", target, path, target)
		}
		return fmt.Sprintf("%q delegated this task to you (%s); handing it back would loop. "+
			"Do it with the tools you have, or say in your answer what you could not do and why.", target, path)
	}
	return ""
}

// offeredAgents is the roster a delegation tool shows this turn: all of it
// for the turn a person is having, and without the agents already working
// on the task for a delegated one.
func offeredAgents(ctx context.Context, agents map[string]config.AgentConfig) map[string]config.AgentConfig {
	if taskDepthFromContext(ctx) == 0 || len(agentChain(ctx)) == 0 {
		return agents
	}
	out := make(map[string]config.AgentConfig, len(agents))
	for name, a := range agents {
		if delegationRefusal(ctx, name) == "" {
			out[name] = a
		}
	}
	return out
}
