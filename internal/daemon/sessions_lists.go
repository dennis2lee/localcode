package daemon

import (
	"net/http"
	"sort"

	"localcode/internal/agent"
)

// AgentInfo is the client-facing view of a configured agent — enough to
// build a picker (TUI Tab-cycle, Web UI dropdown) without exposing the
// full config.AgentConfig (system prompt, tool list). Model is resolved
// from the agent's profile so clients can show e.g. "agent: explore ·
// model: qwen3-30b-a3b" without needing their own copy of config.json.
type AgentInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Model       string `json:"model,omitempty"`
	// Builtin marks one of the Smart Agent specialists rather than an
	// agent somebody declared. They come and go with the switch, which
	// is worth a client saying rather than leaving a name to vanish out
	// of a list with no explanation.
	Builtin bool `json:"builtin,omitempty"`
}

// handleListAgents returns every agent defined in config.json's agents
// map, sorted by name — the picklist for switching a session's active
// agent (e.g. plan -> build).
func (d *Daemon) handleListAgents(w http.ResponseWriter, r *http.Request) {
	// Everything a turn could actually run as, which is the same set the
	// Task tools may target: the declared agents, plus the Smart Agent
	// specialists while that switch is on.
	//
	// It used to be Config.Agents alone, and the six specialists were
	// therefore delegatable but not selectable — Tab, the Web UI menu and
	// "localcode run --agent oracle" could not reach any of them. Nothing
	// below this list was ever the obstacle: profileFor and agentConfig
	// have always resolved a specialist by name.
	all := d.Loop.DelegatableAgents(r.Context())
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]AgentInfo, 0, len(names))
	for _, name := range names {
		agentCfg := all[name]
		_, declared := d.Loop.Config.Agents[name]
		info := AgentInfo{Name: name, Description: agentCfg.Description, Builtin: !declared}
		// ResolveProfile, not a direct Profiles lookup: it is what the turn
		// itself calls, so it also applies the default_profile fallback for
		// an agent whose profile key is missing or unknown. Looking the map
		// up directly reported no model at all for those agents, while the
		// turn went ahead and answered with the default profile's.
		if profile, err := d.Loop.Config.ResolveProfile(name); err == nil && declared {
			info.Model = profile.Model
		} else if p, ok := d.Loop.Config.Profiles[agentCfg.Profile]; ok {
			// A specialist is not in Config.Agents, so ResolveProfile
			// would fall through to the default profile and report every
			// specialist as running on the session's own model — which is
			// most of the cost of Smart Agent and none of the benefit.
			info.Model = p.Model
		}
		out = append(out, info)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleListSlashCommands returns the commands the daemon answers
// itself, so a client can complete one without a hardcoded copy of the
// list. The clients already fetch skills and custom commands; this is
// the third kind, and the only one they had no way to ask about.
func (d *Daemon) handleListSlashCommands(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, agent.SlashCommands())
}

// SkillInfo is the client-facing view of an installed skill.
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// handleListSkills returns every installed skill, name and description
// only.
//
// It exists because both clients had to be told what a skill is called
// before they could offer to complete one, and neither had any way to
// ask: "/skill" is answered on the daemon and its listing arrives as
// transcript text, which is a thing to read rather than a thing to
// complete against. The body is what a skill is called and what it is
// for, never a skill's body, which can be long and is not a listing.
func (d *Daemon) handleListSkills(w http.ResponseWriter, r *http.Request) {
	list := d.Loop.SkillList()
	out := make([]SkillInfo, 0, len(list))
	for _, s := range list {
		out = append(out, SkillInfo{Name: s.Name, Description: s.Description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

// CommandInfo is the client-facing view of a loaded custom slash command.
type CommandInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// handleListCommands returns every custom command loaded from
// .localcode/commands/*.md (project) and ~/.localcode/commands/*.md
// (global) — for a /help listing or client-side autocomplete. Actually
// running a command still goes through POST .../messages like any other
// message; the server matches "/<name>" there.
func (d *Daemon) handleListCommands(w http.ResponseWriter, r *http.Request) {
	out := make([]CommandInfo, 0, len(d.Loop.Commands))
	for _, c := range d.Loop.Commands {
		out = append(out, CommandInfo{Name: c.Name, Description: c.Description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}
