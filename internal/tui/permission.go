package tui

import "fmt"

type pendingPermission struct {
	id, tool, description string
	// rule is the pattern a "session" or "always" answer would grant
	// (e.g. "git *" for a bash call, or the exact path for a file tool) —
	// shown in the prompt so approving a wider scope is an informed
	// choice, not a guess.
	rule string
	// canAlways is false when the daemon has no config.json path to write
	// to (started with neither --config nor a resolvable global config),
	// in which case "always" isn't offered — only once/session/deny.
	canAlways bool
}

// prompt renders the permission modal's single line, listing exactly the
// answers this request will accept — "a" only appears when the daemon
// actually has somewhere to persist it.
func (p pendingPermission) prompt() string {
	keys := "y: allow once  n: deny  s: allow for session"
	if p.canAlways {
		keys += fmt.Sprintf("  a: always allow %q", p.rule)
	}
	return fmt.Sprintf("Permission request [%s]: %s\n%s", p.tool, p.description, keys)
}
