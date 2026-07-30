package config

import "strings"

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
//
// A direct two-pointer walk rather than compiling a regexp: this runs on
// the hot path of every tool call (resolveShellCommand calls it once per
// shell segment times per configured rule, and MatchesPrompt once per
// auto-delegate pattern per prompt), and pattern/subject are plain byte
// strings with no need for a general regex engine.
func globMatch(pattern, subject string) bool {
	p, s := 0, 0
	starP, starS := -1, 0
	for s < len(subject) {
		switch {
		case p < len(pattern) && (pattern[p] == subject[s] || pattern[p] == '?'):
			p++
			s++
		case p < len(pattern) && pattern[p] == '*':
			starP, starS = p, s
			p++
		case starP >= 0:
			// The last "*" seen can absorb one more character of subject —
			// backtrack to just after it and retry from one character
			// further along.
			starS++
			p, s = starP+1, starS
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}
