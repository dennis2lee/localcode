package smart

import (
	"testing"

	"localcode/internal/tools"
)

// The guard the tool-name constants promise.
//
// A specialist's allowlist is matched by string against the names the
// registry registers under, and a name that matches nothing is not an
// error anywhere: SpecsFor silently skips what it does not recognise and
// IsAllowed silently refuses it. The failure is an agent quietly missing
// a tool it was meant to have, which shows up as a sub-agent that cannot
// do its job for reasons nothing reports.
//
// The built-in names are also not uniform — the file and shell tools are
// snake_case, Skill and the delegation tools are not — which is exactly
// the kind of thing a constant gets wrong.
func TestEverySpecialistToolNameIsARealTool(t *testing.T) {
	registry := tools.NewRegistry(nil)
	registry.Register(tools.ReadFile{})
	registry.Register(tools.WriteFile{})
	registry.Register(tools.Edit{})
	registry.Register(tools.Bash{})
	registry.Register(tools.Glob{})
	registry.Register(tools.Grep{})
	registry.Register(tools.NewSkillTool(nil))

	known := map[string]bool{}
	for _, name := range registry.Names() {
		known[name] = true
	}
	// The delegation tools live in internal/agent, which cannot be
	// imported from here without a cycle, so they are named rather than
	// registered. internal/agent's own tests check the other direction.
	for _, name := range DelegationTools {
		known[name] = true
	}

	for _, a := range Builtins() {
		for _, tool := range a.Tools {
			if !known[tool] {
				t.Errorf("%s is given %q, which is not a tool localcode registers", a.Name, tool)
			}
		}
	}
}
