package config

import (
	"encoding/json"
	"fmt"
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

// MarshalJSON writes a ToolPermission back in whichever of its two shapes
// it holds, so round-tripping config.json through AddPermissionRuleToFile
// doesn't rewrite a plain "allow" string as a one-element array.
func (t ToolPermission) MarshalJSON() ([]byte, error) {
	if t.Flat != "" {
		return json.Marshal(string(t.Flat))
	}
	return json.Marshal(t.Rules)
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

// addRule appends rule to m[tool], first promoting a legacy flat decision
// to an explicit "*" rule, and refusing an exact duplicate so repeated
// approvals don't grow the list forever. Shared by the runtime
// (AddPermissionRuleRuntime) and file (AddPermissionRuleToFile) writers, so
// the two can never drift apart on what "add a rule" means.
func addRule(m map[string]ToolPermission, tool string, rule PermissionRule) {
	tp := m[tool]
	if tp.Flat != "" {
		tp.Rules = []PermissionRule{{Match: "*", Decision: tp.Flat}}
		tp.Flat = ""
	}
	for _, existing := range tp.Rules {
		if existing.Match == rule.Match && existing.Decision == rule.Decision {
			return // already covered, don't grow the file on every approval
		}
	}
	tp.Rules = append(tp.Rules, rule)
	m[tool] = tp
}

// removeRule removes the rule matching (Match, Decision) exactly from
// m[tool], dropping the tool's key entirely once its last rule is gone
// rather than leaving an empty array behind. Shared by the runtime
// (RemovePermissionRuleRuntime) and file (RemovePermissionRuleFromFile)
// writers.
func removeRule(m map[string]ToolPermission, tool string, rule PermissionRule) {
	tp, ok := m[tool]
	if !ok {
		return
	}
	if tp.Flat != "" {
		if tp.Flat == rule.Decision && rule.Match == "*" {
			delete(m, tool)
		}
		return
	}
	// A fresh slice rather than filtering tp.Rules in place: the in-place
	// filter (tp.Rules[:0]) aliases the caller's backing array, which for
	// the runtime path is the live config's — anything else holding that
	// slice (e.g. a snapshot taken before this call) would see it mutated
	// out from under it.
	kept := make([]PermissionRule, 0, len(tp.Rules))
	for _, existing := range tp.Rules {
		if existing.Match == rule.Match && existing.Decision == rule.Decision {
			continue
		}
		kept = append(kept, existing)
	}
	if len(kept) == 0 {
		delete(m, tool)
	} else {
		tp.Rules = kept
		m[tool] = tp
	}
}

// decodePermissions parses a "permission" block's individual per-tool keys
// out of the surrounding raw JSON object into typed ToolPermission values.
func decodePermissions(block map[string]json.RawMessage) (map[string]ToolPermission, error) {
	perms := map[string]ToolPermission{}
	for tool, raw := range block {
		var tp ToolPermission
		if err := json.Unmarshal(raw, &tp); err != nil {
			return nil, err
		}
		perms[tool] = tp
	}
	return perms, nil
}

// encodePermissions writes perms back into block, replacing its previous
// contents entirely (a removed tool must disappear, not just go unwritten).
func encodePermissions(block map[string]json.RawMessage, perms map[string]ToolPermission) error {
	for k := range block {
		delete(block, k)
	}
	for tool, tp := range perms {
		encoded, err := json.Marshal(tp)
		if err != nil {
			return fmt.Errorf("marshal permission for %q: %w", tool, err)
		}
		block[tool] = encoded
	}
	return nil
}

// BashToolName is the one tool whose subject is a shell command rather
// than a plain string, so its permission subject needs to be taken apart
// before matching. See ResolvePermission.
const BashToolName = "bash"

// ResolvePermission decides whether a call to toolName is allowed
// automatically, must ask the user, or is denied outright. subject is
// whatever pattern-matchable string the tool exposes for this call (e.g.
// the bash command, or a file path) — "" if the tool has none. Precedence:
// an exact rule for toolName, then a "*" fallback rule, then
// staticRequiresPermission (the tool's own hardcoded default), preserving
// exactly today's behavior for anyone with no "permission" config at all.
//
// The bash tool is special-cased: its subject is a shell command, so one
// "subject" can actually be several commands glued together, and matching
// the raw string against a glob would let anything ride along behind an
// allowed prefix. See resolveShellCommand.
func (c *Config) ResolvePermission(toolName, subject string, staticRequiresPermission bool) Decision {
	if toolName == BashToolName && subject != "" {
		return c.resolveShellCommand(subject, staticRequiresPermission)
	}
	return c.resolveOne(toolName, subject, staticRequiresPermission)
}

func (c *Config) resolveOne(toolName, subject string, staticRequiresPermission bool) Decision {
	d := c.resolveOneStrict(toolName, subject, staticRequiresPermission)
	// skip_permissions downgrades "ask" to "allow" but never touches
	// "deny": a rule written specifically to forbid something keeps
	// forbidding it. Skipping confirmations is a convenience; overriding
	// an explicit prohibition would be a different, much worse promise.
	if d == DecisionAsk && c.PermissionsSkipped() {
		return DecisionAllow
	}
	return d
}

func (c *Config) resolveOneStrict(toolName, subject string, staticRequiresPermission bool) Decision {
	c.permMu.RLock()
	tp, ok := c.Permissions[toolName]
	fallback, hasFallback := c.Permissions["*"]
	c.permMu.RUnlock()
	if ok {
		if d, matched := tp.resolve(subject); matched {
			return d
		}
	}
	if hasFallback {
		if d, matched := fallback.resolve(subject); matched {
			return d
		}
	}
	// Smart Agent's shipped guards, after the user's own rules so any of
	// them can be turned off by writing a rule for the same tool, and
	// before the ordinary builtins because they are the stricter answer.
	if c.SmartAgentLive() {
		if d, matched := secretGuard(toolName, subject); matched {
			return d
		}
	}
	if d, matched := builtinDefault(toolName, subject); matched {
		return d
	}
	if staticRequiresPermission {
		return DecisionAsk
	}
	return DecisionAllow
}

// builtinRules are shipped defaults that apply when nothing in the user's
// own "permission" config has an opinion. They exist so the common case
// works without configuration; any rule in config.json for the same tool
// takes precedence, so setting e.g. {"bash": [{"match":"*","decision":"ask"}]}
// turns them all back off.
//
// git is allowed outright because an agent that has to ask before every
// "git status" is unusable, and because git is the one command set where
// almost everything is either read-only or recoverable through the reflog.
// This is only safe alongside resolveShellCommand: matched against the raw
// command string, "git *" would also green-light "git status && rm -rf ~".
var builtinRules = map[string][]PermissionRule{
	BashToolName: {
		{Match: "git", Decision: DecisionAllow},
		{Match: "git *", Decision: DecisionAllow},
	},
}

// secretPatterns are the paths an agent has no business reading or
// writing on its own.
//
// The threat is the ordinary one and it does not need a malicious model to
// happen: a summarised repository, a pasted stack trace, a "what is in
// this directory" that walks into ~/.aws. Once a credential is in a
// context it has been sent to a provider, and there is no taking it back.
//
// Deny rather than ask, because "may I read your SSH private key?" is a
// question with one right answer and asking it teaches people to click
// yes. Any rule in config.json for the same tool overrides these, which is
// how somebody who genuinely needs the agent to edit a .env says so.
//
// Matched against the path as the model wrote it, with "*" matching any
// run of characters including "/" — so "*.env" catches ".env",
// "config/.env" and "/home/u/app/.env" alike.
var secretPatterns = []string{
	"*.env", "*.env.*", ".env", ".env.*",
	"*id_rsa*", "*id_ed25519*", "*id_ecdsa*", "*id_dsa*",
	"*.pem", "*.key", "*.p12", "*.pfx", "*.keystore",
	"*/.ssh/*", "*/.aws/credentials", "*/.aws/config",
	"*/.gnupg/*", "*/.kube/config", "*/.docker/config.json",
	"*credentials.json", "*/.netrc", ".netrc", "*.htpasswd",
	"*/.npmrc", "*/.pypirc", "*service-account*.json",
}

// secretGuardedTools are the tools that take a path and can therefore be
// checked. bash is deliberately not one of them: a shell command is not a
// path, and pattern-matching "cat .env" out of an arbitrary command line
// is the kind of guard that catches the honest case and misses every other
// one. The shell is governed by its own rules, which is what
// resolveShellCommand is for.
var secretGuardedTools = map[string]bool{
	"read_file": true, "write_file": true, "edit": true,
}

// secretGuard reports a shipped deny for a path that looks like a secret.
func secretGuard(toolName, subject string) (Decision, bool) {
	if subject == "" || !secretGuardedTools[toolName] {
		return "", false
	}
	lowered := strings.ToLower(subject)
	for _, pattern := range secretPatterns {
		if globMatch(pattern, lowered) {
			return DecisionDeny, true
		}
	}
	return "", false
}

func builtinDefault(toolName, subject string) (Decision, bool) {
	rules, ok := builtinRules[toolName]
	if !ok {
		return "", false
	}
	return ToolPermission{Rules: rules}.resolve(subject)
}

// PermissionRuleFor proposes the rule that "always allow" should write for
// one tool call. For bash it generalizes to the command's first word
// ("npm *" from "npm test"), because approving a shell command usually
// means approving that program rather than that exact argument list. Every
// other tool keeps the exact subject, since a file path approval that
// silently widened to a whole directory would be a nasty surprise.
//
// Callers show this pattern to the user before writing it, so the scope
// being granted is visible rather than inferred.
func PermissionRuleFor(toolName, subject string) PermissionRule {
	if subject == "" {
		return PermissionRule{Match: "*", Decision: DecisionAllow}
	}
	if toolName == BashToolName {
		fields := strings.Fields(subject)
		if len(fields) > 0 {
			if len(fields) == 1 {
				return PermissionRule{Match: fields[0], Decision: DecisionAllow}
			}
			return PermissionRule{Match: fields[0] + " *", Decision: DecisionAllow}
		}
	}
	return PermissionRule{Match: subject, Decision: DecisionAllow}
}
