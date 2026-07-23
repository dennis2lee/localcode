package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	raw := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse config %s: %w", path, err)
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("read config %s: %w", path, err)
	}

	perms := map[string]ToolPermission{}
	if rawPerms, ok := raw["permission"]; ok {
		if err := json.Unmarshal(rawPerms, &perms); err != nil {
			return fmt.Errorf("parse permission in %s: %w", path, err)
		}
	}

	tp := perms[toolName]
	if tp.Flat != "" {
		// A flat decision covered every subject. Preserve that meaning as
		// an explicit catch-all before appending, rather than throwing it
		// away and silently widening or narrowing the policy.
		tp.Rules = []PermissionRule{{Match: "*", Decision: tp.Flat}}
		tp.Flat = ""
	}
	for _, existing := range tp.Rules {
		if existing.Match == rule.Match && existing.Decision == rule.Decision {
			return nil // already covered, don't grow the file on every approval
		}
	}
	tp.Rules = append(tp.Rules, rule)
	perms[toolName] = tp

	encoded, err := json.Marshal(perms)
	if err != nil {
		return fmt.Errorf("marshal permission: %w", err)
	}
	raw["permission"] = encoded

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	out = append(out, '\n')
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
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
