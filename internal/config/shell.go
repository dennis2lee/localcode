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
		switch c.resolveSegment(segment, staticRequiresPermission) {
		case DecisionDeny:
			return DecisionDeny
		case DecisionAsk:
			worst = DecisionAsk
		}
	}
	if worst == DecisionAllow && hasUnsafeShellConstruct(command) {
		// skip_permissions reaches this ask too.
		//
		// It did not, and the result was that turning "skip all
		// permission prompts" on stopped almost nothing: every segment
		// resolves to allow (resolveOne applies the downgrade), and then
		// this line forced the whole line back to ask. A redirect or a
		// substitution is in a large share of the commands an agent
		// writes, and `>` matches anywhere in the string — so the prompt
		// kept coming for a setting whose own checkbox reads "every tool
		// call runs without asking; explicit deny rules still deny".
		//
		// Deny is unaffected: it returns above, before this is reached.
		// That is the whole of what skip is promised not to override,
		// and it is still true.
		if c.PermissionsSkipped() {
			return DecisionAllow
		}
		return DecisionAsk
	}
	return worst
}

// unsafeShellConstructs never auto-allow. Substitutions run a nested
// command that segment splitting never sees, and redirections turn a
// read-only-looking command into a file write.
var unsafeShellConstructs = []string{"$(", "`", "<(", ">(", ">"}

// resolveSegment decides one command, matching it both as written and
// with its shell quoting removed.
//
// A rule is a glob against text, and the text still carries its quotes —
// but the shell strips them before deciding what to run. So `curl x`,
// `"curl" x`, `cu''rl x` and `c\url x` are one command wearing four
// spellings, and a rule `curl *` only recognised the first. Normally that
// only weakened a deny to a prompt; with skip_permissions on, the prompt
// resolves to allow, so an explicitly denied command ran unprompted. That
// is not a bypass a user can be expected to anticipate: nothing on screen
// distinguishes the spellings.
//
// Both readings are consulted and the stricter answer wins, which is what
// keeps this from cutting the other way. Deny needs only one of them, so
// no spelling escapes a prohibition; allow needs both, so unquoting can
// never widen a permission — a rule the user did not write to cover the
// literal text cannot start matching it now.
func (c *Config) resolveSegment(segment string, staticRequiresPermission bool) Decision {
	raw := c.resolveOne(BashToolName, segment, staticRequiresPermission)
	unquoted := unquoteShellText(segment)
	if unquoted == segment {
		return raw
	}
	norm := c.resolveOne(BashToolName, unquoted, staticRequiresPermission)
	if raw == DecisionDeny || norm == DecisionDeny {
		return DecisionDeny
	}
	if raw == DecisionAllow && norm == DecisionAllow {
		return DecisionAllow
	}
	return DecisionAsk
}

// unquoteShellText removes one level of shell quoting, the way the shell
// does before a command is run: outside quotes a backslash makes the next
// character literal, single quotes are literal throughout with no
// escaping at all inside them, and double quotes allow backslash escapes.
//
// This is for matching only. It is not a general shell parser and does no
// expansion — a variable or a substitution is left exactly as written,
// since resolving what those stand for is not something a permission
// check can do, and hasUnsafeShellConstruct already refuses to auto-allow
// them.
func unquoteShellText(segment string) string {
	var b strings.Builder
	b.Grow(len(segment))
	runes := []rune(segment)
	var quote rune
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case quote == '\'':
			if r == '\'' {
				quote = 0
				continue
			}
			b.WriteRune(r)
		case quote == '"':
			if r == '\\' && i+1 < len(runes) {
				i++
				b.WriteRune(runes[i])
				continue
			}
			if r == '"' {
				quote = 0
				continue
			}
			b.WriteRune(r)
		case r == '\\':
			if i+1 < len(runes) {
				i++
				b.WriteRune(runes[i])
			}
		case r == '\'' || r == '"':
			quote = r
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

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
// belongs to.
//
// Backslash escaping is modelled the way bash does it, and getting that
// wrong was a permission bypass rather than a cosmetic difference. This
// used to honour "\" only inside double quotes; outside them a "\"" was
// written through and the quote that followed it opened a quoted region
// that swallowed the rest of the line. So:
//
//	git status \" && rm -rf ~
//
// split into ONE segment, which the built-in "git *" rule matched, and
// the whole line was auto-allowed with no prompt — while bash, for which
// \" is a literal quote character and && a live operator, duly ran the
// rm. Any allowed prefix was a launch point for anything appended to it,
// which is the exact attack this function exists to stop.
//
// The rules, matching bash: outside quotes a backslash makes the next
// character literal (so it can neither open a quote nor act as a
// separator); inside double quotes it likewise escapes the next
// character; inside single quotes there is no escaping at all.
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
		case '\\':
			// The next character is literal, whatever it is. Both are
			// kept in the segment text so it still reads as the user
			// wrote it for glob matching; what matters is that neither
			// is interpreted here.
			cur.WriteRune(r)
			if i+1 < len(runes) {
				i++
				cur.WriteRune(runes[i])
			}
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
