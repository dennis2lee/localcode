package agent

import (
	"localcode/internal/config"
	"localcode/internal/smart"
)

// Smart Agent, from the loop's side.
//
// internal/smart owns what the specialists are; this file owns when they
// exist. The distinction matters because the setting is live: someone can
// turn Smart Agent on in the middle of a session and the next turn has to
// see six new agents and two new tools, without the daemon restarting and
// without the config on disk being rewritten underneath a turn already in
// flight. So nothing is merged into Config at startup. The roster is
// derived per turn from a boolean, and turning the boolean off puts every
// one of them away again.

// SmartAgentEnabled reports whether Smart Agent is on — process-global,
// toggleable live via "/config smart_agent on|off" and from the settings
// panel.
// Held on Config rather than in Loop.settings beside the other live
// toggles, because permission resolution needs it too and runs with a
// *Config and no Loop in sight (see cmd/localcode/wire.go). One switch,
// one place, read under one lock.
func (l *Loop) SmartAgentEnabled() bool { return l.Config.SmartAgentLive() }

// SetSmartAgentEnabled changes the live Smart Agent setting. It takes
// effect on the next turn: the specialists, the orchestration prompt and
// the background delegation tools all appear and disappear together.
func (l *Loop) SetSmartAgentEnabled(v bool) { l.Config.SetSmartAgentRuntime(v) }

// smartAgents is the built-in roster as it stands right now — empty when
// Smart Agent is off, and never containing a name the user's own config
// already defines.
func (l *Loop) smartAgents() map[string]config.AgentConfig {
	if !l.SmartAgentEnabled() {
		return nil
	}
	return smart.Agents(l.Config)
}

// DelegatableAgents is every agent the Task tools may target: the user's
// own, plus the built-in specialists when Smart Agent is on.
//
// Exported because the Task tool asks for it on every call rather than
// being handed a snapshot at startup — that snapshot is exactly what
// would go stale the moment the setting is flipped, leaving the tool
// advertising an enum of agents that no longer exist.
func (l *Loop) DelegatableAgents() map[string]config.AgentConfig {
	smartOnes := l.smartAgents()
	if len(smartOnes) == 0 {
		return l.Config.Agents
	}
	out := make(map[string]config.AgentConfig, len(l.Config.Agents)+len(smartOnes))
	for name, a := range l.Config.Agents {
		out[name] = a
	}
	for name, a := range smartOnes {
		out[name] = a
	}
	return out
}

// agentConfig is the config for a named agent, user-defined first. A name
// that is neither returns the zero value, which is the no-op it has
// always been: no extra prompt, no tool restriction.
func (l *Loop) agentConfig(agentName string) config.AgentConfig {
	if a, ok := l.Config.Agents[agentName]; ok {
		return a
	}
	return l.smartAgents()[agentName]
}

// profileFor resolves the model profile a turn running as agentName uses.
//
// Config.ResolveProfile cannot answer for a built-in specialist: those
// are not in Config.Agents, so it would fall through to the default
// profile and every specialist would run on the session's own model —
// which is most of the cost of Smart Agent and none of the benefit.
// The profile's name comes back with it because a fallback chain is
// looked up by name (see fallback.go), and because the trace records
// which profile answered rather than only which model.
func (l *Loop) profileFor(agentName string) (string, config.Profile, error) {
	if _, mine := l.Config.Agents[agentName]; !mine {
		if sa, ok := l.smartAgents()[agentName]; ok {
			if p, ok := l.Config.Profiles[sa.Profile]; ok {
				return sa.Profile, p, nil
			}
		}
	}
	p, err := l.Config.ResolveProfile(agentName)
	return l.profileName(agentName), p, err
}

// profileName is the key in Config.Profiles that ResolveProfile would land
// on for an agent: the agent's own, or the default.
func (l *Loop) profileName(agentName string) string {
	if a, ok := l.Config.Agents[agentName]; ok {
		if _, ok := l.Config.Profiles[a.Profile]; ok {
			return a.Profile
		}
	}
	return l.Config.DefaultProfile
}

// toolsForTurn is the tool allowlist for this turn: the agent's own
// restriction if it has one, otherwise everything the registry holds
// minus the delegation tools that would be noise right now.
//
// Returning a concrete list rather than a "hide these" set keeps one
// mechanism instead of two — the specs the model is shown and the check
// in runTools both already speak allowlist, and both get this same slice,
// so a tool hidden from the model is also refused if it is called anyway.
func (l *Loop) toolsForTurn(agentCfg config.AgentConfig) []string {
	if len(agentCfg.Tools) > 0 {
		return agentCfg.Tools
	}
	hidden := l.hiddenDelegationTools()
	if len(hidden) == 0 || l.Tools == nil {
		return nil
	}
	names := l.Tools.Names()
	out := make([]string, 0, len(names))
	for _, name := range names {
		if !hidden[name] {
			out = append(out, name)
		}
	}
	return out
}

// hiddenDelegationTools names the delegation tools that should not be
// offered on this turn.
//
// Two separate reasons, and they hide different amounts:
//
//   - Nowhere to delegate. A config with one agent and Smart Agent off has
//     no second role to hand work to, so Task would be an expensive way
//     for a model to call itself. This is the pre-existing rule, which
//     used to be enforced by simply not registering the tool.
//   - Smart Agent off. Background delegation is part of the bundle rather
//     than a general capability: launching work that runs unattended, in
//     a session nobody is looking at, is the thing a user opts into.
func (l *Loop) hiddenDelegationTools() map[string]bool {
	hidden := map[string]bool{}
	if !l.SmartAgentEnabled() {
		hidden[smart.ToolSpawn] = true
		hidden[smart.ToolCollect] = true
	}
	if len(l.DelegatableAgents()) < 2 {
		for _, name := range smart.DelegationTools {
			hidden[name] = true
		}
	}
	return hidden
}

// orchestrationFor returns the orchestration prompt for this turn, or ""
// when this turn is not an orchestrator.
//
// Three things disqualify a turn, and each is a real case rather than a
// precaution:
//
//   - It is running as one of the specialists. A sub-agent told to
//     decompose and delegate is the recursive-explosion problem, and it
//     is already denied the tools to do it; giving it the prompt as well
//     would just have it narrate an orchestration it cannot perform.
//   - It is a child session. Task and auto-delegation both run in child
//     sessions, so this catches a user's own agent being run as somebody
//     else's sub-agent, which the check above cannot see.
//   - There is nobody to delegate to. Without a second agent the prompt
//     describes a Task tool the model has not been given.
func (l *Loop) orchestrationFor(sessionID, agentName, model string) string {
	if !l.SmartAgentEnabled() {
		return ""
	}
	if _, specialist := l.smartAgents()[agentName]; specialist {
		return ""
	}
	if l.Store != nil {
		if sess, err := l.Store.Get(sessionID); err == nil && sess.ParentID != "" {
			return ""
		}
	}
	if len(l.DelegatableAgents()) < 2 {
		return ""
	}
	return smart.OrchestrationPrompt(model)
}
