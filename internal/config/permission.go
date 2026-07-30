package config

import (
	"encoding/json"
	"fmt"
	"os"
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

func builtinDefault(toolName, subject string) (Decision, bool) {
	rules, ok := builtinRules[toolName]
	if !ok {
		return "", false
	}
	return ToolPermission{Rules: rules}.resolve(subject)
}

// resolveShellCommand resolves a whole bash command line by resolving
// every command in it separately and taking the most cautious answer.
//
// Matching the raw string would make an allow rule far broader than it
// reads: "git *" as a glob also matches "git status && rm -rf ~", so one
// innocuous-looking prefix would auto-run anything appended to it. Each
// segment therefore has to earn "allow" on its own, any deny anywhere
// denies the whole line, and anything less than unanimous allow falls
// back to asking.
//
// Constructs that can smuggle a command past segment splitting entirely
// (command substitution, process substitution) or write to arbitrary
// files (output redirection) are never auto-allowed; they downgrade to
// ask. A deny still wins over them, so an explicit deny rule cannot be
// escaped by adding a redirect.
func (c *Config) resolveShellCommand(command string, staticRequiresPermission bool) Decision {
	worst := DecisionAllow
	for _, segment := range splitShellSegments(command) {
		if segment == "" {
			continue
		}
		switch c.resolveOne(BashToolName, segment, staticRequiresPermission) {
		case DecisionDeny:
			return DecisionDeny
		case DecisionAsk:
			worst = DecisionAsk
		}
	}
	if worst == DecisionAllow && hasUnsafeShellConstruct(command) {
		return DecisionAsk
	}
	return worst
}

// unsafeShellConstructs never auto-allow. Substitutions run a nested
// command that segment splitting never sees, and redirections turn a
// read-only-looking command into a file write.
var unsafeShellConstructs = []string{"$(", "`", "<(", ">(", ">"}

func hasUnsafeShellConstruct(command string) bool {
	for _, c := range unsafeShellConstructs {
		if strings.Contains(command, c) {
			return true
		}
	}
	return false
}

// splitShellSegments breaks a command line on the operators that chain
// separate commands together ("&&", "||", ";", "|", and newlines), while
// respecting single and double quotes so a separator inside an argument
// (a commit message like -m "fix: a; b") doesn't split the command it
// belongs to. Escaping is honored inside double quotes only, matching the
// shell.
func splitShellSegments(command string) []string {
	var segments []string
	var cur strings.Builder
	var quote rune // 0 when not inside quotes

	flush := func() {
		segments = append(segments, strings.TrimSpace(cur.String()))
		cur.Reset()
	}

	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		r := runes[i]

		if quote != 0 {
			if r == '\\' && quote == '"' && i+1 < len(runes) {
				cur.WriteRune(r)
				i++
				cur.WriteRune(runes[i])
				continue
			}
			if r == quote {
				quote = 0
			}
			cur.WriteRune(r)
			continue
		}

		switch r {
		case '\'', '"':
			quote = r
			cur.WriteRune(r)
		case '\n', ';':
			flush()
		case '&', '|':
			// "&&" and "||" chain; a single "|" pipes. All three start a
			// new command, so all three split. A lone "&" backgrounds the
			// command before it, which also ends it.
			if i+1 < len(runes) && runes[i+1] == r {
				i++
			}
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return segments
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

// MarshalJSON writes a ToolPermission back in whichever of its two shapes
// it holds, so round-tripping config.json through AddPermissionRuleToFile
// doesn't rewrite a plain "allow" string as a one-element array.
func (t ToolPermission) MarshalJSON() ([]byte, error) {
	if t.Flat != "" {
		return json.Marshal(string(t.Flat))
	}
	return json.Marshal(t.Rules)
}

// AddPermissionRuleToFile appends one rule to path's "permission" map for
// toolName, creating the file and the map as needed. It rewrites only the
// "permission" key and leaves every other key in the file byte-for-byte
// alone, the same surgical approach UpdateMCPServersInFile takes, so a
// field this build doesn't know about (a typo, a newer version's setting)
// isn't silently dropped when the user picks "always allow".
//
// The rule is appended rather than inserted because ToolPermission
// resolves with last-match-wins: a later rule is what overrides an earlier
// broader one.
func AddPermissionRuleToFile(path, toolName string, rule PermissionRule) error {
	return updateRawSection(path, "permission", func(block map[string]json.RawMessage) error {
		perms, err := decodePermissions(block)
		if err != nil {
			return fmt.Errorf("parse permission in %s: %w", path, err)
		}
		addRule(perms, toolName, rule)
		return encodePermissions(block, perms)
	})
}

// RemovePermissionRuleFromFile removes one rule from path's "permission"
// map for toolName, matched by exact (Match, Decision). It leaves every
// other key untouched, the same surgical approach as AddPermissionRuleToFile.
// Removing the last rule for a tool drops that tool's key entirely rather
// than leaving an empty array behind.
func RemovePermissionRuleFromFile(path, toolName string, rule PermissionRule) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil // no file, nothing to remove
	}
	return updateRawSection(path, "permission", func(block map[string]json.RawMessage) error {
		perms, err := decodePermissions(block)
		if err != nil {
			return fmt.Errorf("parse permission in %s: %w", path, err)
		}
		removeRule(perms, toolName, rule)
		return encodePermissions(block, perms)
	})
}

// SetSkipPermissionsInFile writes the top-level "skip_permissions" key,
// leaving every other key untouched. See Config.SkipPermissions.
func SetSkipPermissionsInFile(path string, enabled bool) error {
	return updateRawConfig(path, func(raw map[string]json.RawMessage) error {
		encoded, err := json.Marshal(enabled)
		if err != nil {
			return fmt.Errorf("marshal skip_permissions: %w", err)
		}
		raw["skip_permissions"] = encoded
		return nil
	})
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

// SetAutoDelegateEnabledInFile flips only the "enabled" field inside the
// top-level "auto_delegate" block at path, leaving the rest of that block
// (agent, match) and every other key in the file untouched — the same
// surgical approach the permission writers above take.
//
// Enabling when no auto_delegate block exists yet writes one with just
// {"enabled": true} and no agent. That config is inert (Validate rejects an
// agent-less block, and MatchesPrompt with no patterns delegates nothing),
// which is why callers are expected to tell the user that a block still has
// to be filled in — writing a guessed agent name here would be worse.
func SetAutoDelegateEnabledInFile(path string, enabled bool) error {
	return updateAutoDelegateInFile(path, func(block map[string]json.RawMessage) error {
		encoded, err := json.Marshal(enabled)
		if err != nil {
			return fmt.Errorf("marshal auto_delegate.enabled: %w", err)
		}
		block["enabled"] = encoded
		return nil
	})
}

// SetAutoDelegateTargetInFile writes which agent handles delegated prompts
// and which prompts qualify, leaving "enabled" — and any key this build
// doesn't know about — exactly as it was. The counterpart to
// SetAutoDelegateEnabledInFile, which changes only the switch.
//
// An empty match list is written as an empty array rather than omitted: it
// means "delegate nothing", which is a real choice someone can make from a
// settings panel, and dropping the key would instead read as "unset".
func SetAutoDelegateTargetInFile(path, agent string, match []string) error {
	return updateAutoDelegateInFile(path, func(block map[string]json.RawMessage) error {
		encodedAgent, err := json.Marshal(agent)
		if err != nil {
			return fmt.Errorf("marshal auto_delegate.agent: %w", err)
		}
		block["agent"] = encodedAgent

		if match == nil {
			match = []string{}
		}
		encodedMatch, err := json.Marshal(match)
		if err != nil {
			return fmt.Errorf("marshal auto_delegate.match: %w", err)
		}
		block["match"] = encodedMatch
		return nil
	})
}

// updateAutoDelegateInFile rewrites only the named keys inside the top-level
// "auto_delegate" object, leaving the rest of that block and every other key
// in the file untouched — the same surgical approach the permission writers
// take, so a field a newer version added isn't dropped by an older one.
func updateAutoDelegateInFile(path string, update func(block map[string]json.RawMessage) error) error {
	return updateRawSection(path, "auto_delegate", update)
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
