package config

import (
	"context"
	"strings"
	"unicode"

	"localcode/internal/shell"
)

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
func (c *Config) resolveShellCommand(ctx context.Context, command string, staticRequiresPermission bool) Decision {
	return c.resolveShellCommandUnder(ctx, command, staticRequiresPermission, shell.IsPOSIX())
}

// resolveShellCommandUnder is resolveShellCommand with the one fact about
// the shell it depends on handed to it, so the non-POSIX case can be run
// on a machine whose own shell is sh.
//
// posix is what makes the splitting below sound. splitShellSegments reads
// a command the way a POSIX shell does — ; && || | & separate, and a
// backslash escapes whatever follows — and requires every piece to be
// permitted. Under a shell that reads it differently the pieces are not
// the shell's pieces: in cmd.exe a backslash escapes nothing and & still
// separates, so "git status \& rm -rf /" is one segment here, matches a
// "git *" rule, and runs as two commands there.
//
// So an allow decided by splitting is only trusted where the split is the
// shell's. Everywhere else it becomes ask — the person sees the command
// and answers for it. Deny is untouched, because a deny does not depend on
// the split being right: it needs one piece to match, not all of them.
func (c *Config) resolveShellCommandUnder(ctx context.Context, command string, staticRequiresPermission, posix bool) Decision {
	worst := DecisionAllow
	for _, segment := range shellSegments(command) {
		if segment == "" {
			continue
		}
		switch c.resolveSegment(ctx, segment, staticRequiresPermission) {
		case DecisionDeny:
			return DecisionDeny
		case DecisionAsk:
			worst = DecisionAsk
		}
	}
	if worst == DecisionAllow && !posix {
		// Not subject to skip_permissions, unlike the construct check
		// below. That one downgrades a prompt somebody found noisy; this
		// one is the difference between having read the command and not,
		// and "run everything without asking" is not an answer to a
		// question nobody was in a position to ask.
		return DecisionAsk
	}
	if worst == DecisionAllow && (hasUnsafeShellConstruct(command) || lineRunsGitCommand(command)) {
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
// `"curl" x`, a `curl` with an empty quote pair inside it, and `c\url x`
// are one command wearing four spellings, and a rule `curl *` only
// recognised the first. Normally that
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
func (c *Config) resolveSegment(ctx context.Context, segment string, staticRequiresPermission bool) Decision {
	ctx = context.WithValue(ctx, shellSegmentKey{}, strings.TrimSpace(segment))
	raw := c.resolveOne(ctx, BashToolName, segment, staticRequiresPermission)
	unquoted := unquoteShellText(segment)
	if unquoted == segment {
		return raw
	}
	norm := c.resolveOne(ctx, BashToolName, unquoted, staticRequiresPermission)
	if raw == DecisionDeny || norm == DecisionDeny {
		return DecisionDeny
	}
	if raw == DecisionAllow && norm == DecisionAllow {
		return DecisionAllow
	}
	return DecisionAsk
}

// shellSegmentKey carries the command resolveSegment is deciding, as
// written, to the rules it consults for both readings of it. A literal
// rule is the one command somebody approved, so it is compared with that
// and never with the unquoted reading: there, approving rm "a*b", which
// removes the one file named a*b, would also allow rm a*b, which removes
// every file the shell expands a*b to.
type shellSegmentKey struct{}

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

// GrantSegments is what a remembered "allow for this session" has to
// cover, one by one, for a bash line other than the one answered: each
// command of it, as written and with its quoting removed, the two
// readings resolveSegment requires of a config rule. False for a line no
// grant may cover: one with a substitution, a redirect or a Git command
// that runs another command, which
// resolveShellCommand never allows either. Under a shell that does not
// split the POSIX way only a line made of plain characters is covered,
// whole, since the pieces this would cut it into are not that shell's,
// and fish and PowerShell run a command in parentheses.
func GrantSegments(command string) ([]string, bool) {
	return grantSegmentsUnder(command, shell.IsPOSIX())
}

func grantSegmentsUnder(command string, posix bool) ([]string, bool) {
	if hasUnsafeShellConstruct(command) || lineRunsGitCommand(command) {
		return nil, false
	}
	if !posix {
		trimmed := strings.TrimSpace(command)
		for _, r := range trimmed {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(plainShellRunes, r) {
				return nil, false
			}
		}
		if trimmed == "" {
			return nil, false
		}
		// Unquoted as well, as for POSIX: fish runs "rm" as rm.
		if unquoted := unquoteShellText(trimmed); unquoted != trimmed {
			return []string{trimmed, unquoted}, true
		}
		return []string{trimmed}, true
	}
	var out []string
	for _, segment := range shellSegments(command) {
		if segment == "" {
			continue
		}
		out = append(out, segment)
		if unquoted := unquoteShellText(segment); unquoted != segment {
			out = append(out, unquoted)
		}
	}
	return out, len(out) > 0
}

// plainShellRunes are the characters other than letters and digits that
// no shell localcode runs under reads as an operator, an escape, a
// substitution or a grouping. Not %: cmd.exe expands %VAR:~n,1% to any
// one character of a variable, & included, before it looks for operators.
const plainShellRunes = " -_./\\:=,+~\"'*?@"

func hasUnsafeShellConstruct(command string) bool {
	for _, c := range unsafeShellConstructs {
		if strings.Contains(command, c) {
			return true
		}
	}
	return hasUnquotedParen(command)
}

// hasUnquotedParen reports whether command has a "(" outside quotes
// and escapes in any reading of it (see shellQuotings).
//
// Under zsh a parenthesis in a word makes it a pattern, and a group at
// its end is a glob qualifier: `ls a(e:'rm -rf ~':)` runs the command for
// each file that matches, with no wildcard needed. Under sh and bash an
// unquoted "(" is a subshell, a function definition, an extglob or a
// syntax error, none of which should be allowed by a rule for the word
// before it. A quoted or escaped one is a character to every shell.
func hasUnquotedParen(command string) bool {
	for _, reading := range shellReadings(command) {
		bare := bareRunes(reading.runes, reading.q)
		for i, r := range reading.runes {
			if r == '(' && bare[i] {
				return true
			}
		}
	}
	return false
}

// shellQuoting is one way of reading where a command line's quotes are.
type shellQuoting int

const (
	// posixQuoting: ' and " quote, and a backslash escapes the next
	// character everywhere except inside single quotes. How sh, bash and
	// zsh read a line with none of the constructs below in it.
	posixQuoting shellQuoting = iota
	// ansiCQuoting adds $'...', inside which a backslash escapes the next
	// character, ' included. bash and zsh read it so; a shell without
	// $'...' reads "$" followed by an ordinary single quote, which is
	// posixQuoting.
	ansiCQuoting
	// noQuoting: quotes and backslashes are ordinary characters. That is
	// how a shell reads them in a comment, and quotes in a here-document's
	// body.
	noQuoting
)

// shellReading is one text of a command line read under one quoting.
type shellReading struct {
	runes []rune
	q     shellQuoting
}

// shellReadings are the readings a command line is checked under: the
// POSIX one, and each other one a construct in the line makes possible.
//
// One reading is not enough, and trusting one was a permission bypass.
// Under posixQuoting alone, with the built-in "git *" rule and nothing
// configured, each of these was one command and allowed unasked, while
// bash ran the rm:
//
//	git log $'\'' && rm -rf ~        (the \' is escaped in $'...')
//	git status # '<newline>rm -rf ~<newline>'   (a quote in a comment)
//	git status # \<newline>rm -rf ~           (no escape in a comment)
//	git log $\<newline>'\''; rm -rf ~        (the line is joined first)
//
// and a here-document's body hid commands the same way. Which reading
// the shell uses is not knowable here (dash has no $'...', and whether a
// "#" starts a comment depends on where it is), so every plausible one
// is split and all of their pieces are checked: an allow needs every
// piece of every reading, and one denied piece in any reading denies.
//
// The line is read as written and, when it has one, with each backslash
// newline removed, which the shell does before it looks at quotes.
//
// The quote-blind reading splits at every operator character, so the
// places the shell splits are always among its places. It is taken in
// two cases. When a "#" or a "<<" is bare in another reading: the first
// one the shell acts on is outside quotes in the reading the shell is
// using, so that is enough, and a "#" inside a commit message does not
// turn its parentheses into a prompt. And when the line has a construct
// inside which quotes start over: in "${x:-"'"}" the inner quotes are a
// new pair to every shell, while posixQuoting closes the outer one at
// the first of them and lets the ' hide the rest of the line.
func shellReadings(command string) []shellReading {
	texts := []string{command}
	if joined := strings.ReplaceAll(command, "\\\n", ""); joined != command {
		texts = append(texts, joined)
	}
	var out []shellReading
	for _, text := range texts {
		runes := []rune(text)
		qs := []shellQuoting{posixQuoting}
		if strings.Contains(text, "$'") {
			qs = append(qs, ansiCQuoting)
		}
		blind := hasNestedQuoting(text)
		for _, q := range qs {
			out = append(out, shellReading{runes, q})
			blind = blind || startsCommentOrHereDoc(runes, bareRunes(runes, q))
		}
		if blind {
			out = append(out, shellReading{runes, noQuoting})
		}
	}
	return out
}

// nestedQuotings are the constructs inside which a shell starts its
// quoting over: parameter expansion, command and process substitution,
// and the old arithmetic form.
var nestedQuotings = []string{"${", "$(", "`", "<(", ">(", "$["}

// hasNestedQuoting reports whether text has one of nestedQuotings,
// quoted or not: a parameter expansion is live inside double quotes,
// which is where it is usually written.
func hasNestedQuoting(text string) bool {
	for _, c := range nestedQuotings {
		if strings.Contains(text, c) {
			return true
		}
	}
	return false
}

// startsCommentOrHereDoc reports whether a bare "#" or "<<" is in runes.
func startsCommentOrHereDoc(runes []rune, bare []bool) bool {
	for i, r := range runes {
		if !bare[i] {
			continue
		}
		if r == '#' || (r == '<' && i+1 < len(runes) && runes[i+1] == '<' && bare[i+1]) {
			return true
		}
	}
	return false
}

// shellSegments is splitShellSegments under every reading of command,
// without repeats.
func shellSegments(command string) []string {
	var out []string
	seen := map[string]bool{}
	for _, reading := range shellReadings(command) {
		for _, segment := range splitShellSegments(string(reading.runes), reading.q) {
			if !seen[segment] {
				seen[segment] = true
				out = append(out, segment)
			}
		}
	}
	return out
}

// bareRunes reports, for each rune of a command line read under q,
// whether it is outside quotes and not escaped: the runes a shell can
// read as an operator.
func bareRunes(runes []rune, q shellQuoting) []bool {
	bare := make([]bool, len(runes))
	var quote rune // 0 when not inside quotes
	ansiC := false // the quote is $'...'
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if quote != 0 {
			if r == '\\' && (quote == '"' || ansiC) && i+1 < len(runes) {
				i++
				continue
			}
			if r == quote {
				quote, ansiC = 0, false
			}
			continue
		}
		switch {
		case r == '\\' && q != noQuoting:
			// The next character is literal, whatever it is.
			i++
		case (r == '\'' || r == '"') && q != noQuoting:
			quote = r
			ansiC = q == ansiCQuoting && r == '\'' && i > 0 && runes[i-1] == '$' && bare[i-1]
		default:
			bare[i] = true
		}
	}
	return bare
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
func splitShellSegments(command string, q shellQuoting) []string {
	var segments []string
	var cur strings.Builder

	flush := func() {
		segments = append(segments, strings.TrimSpace(cur.String()))
		cur.Reset()
	}

	runes := []rune(command)
	bare := bareRunes(runes, q)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if !bare[i] {
			// Quoted or escaped: kept in the segment as written, so it
			// still reads as the user wrote it for glob matching, and
			// not interpreted here.
			cur.WriteRune(r)
			continue
		}
		switch r {
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
