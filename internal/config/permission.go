package config

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Decision is a resolved permission outcome for one tool call: run it
// without asking, ask the user, or refuse outright. Values match
// opencode's own "permission" vocabulary.
type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionAsk   Decision = "ask"
	DecisionDeny  Decision = "deny"
)

// PermissionRule pattern-matches a call's "subject" (a bash command, a
// file path — whatever a tool exposes as pattern-matchable; see
// tools.PermissionSubject) against Match, an opencode-style glob ("*"
// matches any run of characters, "?" matches exactly one).
type PermissionRule struct {
	Match    string   `json:"match"`
	Decision Decision `json:"decision"`
}

// ToolPermission is the value of one entry in Config.Permissions. Its JSON
// form is either a bare decision string (applies to every call of that
// tool regardless of subject) or an array of PermissionRule, matched in
// array order with the last match winning — ordered explicitly (rather
// than opencode's object-of-patterns, whose key order Go's JSON decoder
// doesn't preserve into a map) so "last match wins" is unambiguous.
type ToolPermission struct {
	Flat  Decision
	Rules []PermissionRule
}

func (t *ToolPermission) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		t.Flat = Decision(s)
		return nil
	}
	var rules []PermissionRule
	if err := json.Unmarshal(data, &rules); err != nil {
		return fmt.Errorf("permission rule must be a decision string or an array of {\"match\",\"decision\"}: %w", err)
	}
	t.Rules = rules
	return nil
}

// resolve returns the decision this ToolPermission implies for subject,
// and whether it had an opinion at all (false if it's an empty rule list
// that never matched, or a zero-value ToolPermission).
func (t ToolPermission) resolve(subject string) (Decision, bool) {
	if t.Flat != "" {
		return t.Flat, true
	}
	var last Decision
	matched := false
	for _, r := range t.Rules {
		if globMatch(r.Match, subject) {
			last = r.Decision
			matched = true
		}
	}
	return last, matched
}

// ResolvePermission decides whether a call to toolName is allowed
// automatically, must ask the user, or is denied outright. subject is
// whatever pattern-matchable string the tool exposes for this call (e.g.
// the bash command, or a file path) — "" if the tool has none. Precedence:
// an exact rule for toolName, then a "*" fallback rule, then
// staticRequiresPermission (the tool's own hardcoded default), preserving
// exactly today's behavior for anyone with no "permission" config at all.
func (c *Config) ResolvePermission(toolName, subject string, staticRequiresPermission bool) Decision {
	if tp, ok := c.Permissions[toolName]; ok {
		if d, matched := tp.resolve(subject); matched {
			return d
		}
	}
	if tp, ok := c.Permissions["*"]; ok {
		if d, matched := tp.resolve(subject); matched {
			return d
		}
	}
	if staticRequiresPermission {
		return DecisionAsk
	}
	return DecisionAllow
}

// globMatch reports whether subject matches pattern, where "*" matches
// any run of characters (including none) and "?" matches exactly one
// character — a plain, separator-unaware glob, since subjects here are
// shell commands and file paths, not just path segments (so filepath.Match
// semantics, where "*" stops at "/", would be wrong for a bash command).
func globMatch(pattern, subject string) bool {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false // malformed pattern never matches, rather than panicking
	}
	return re.MatchString(subject)
}
