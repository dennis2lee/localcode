package config

import (
	"context"
	"regexp"
	"strings"
)

type denyReachDepthKey struct{}

// These wrappers can run a later word as a command. Try every suffix to
// cover options, their values, and nested wrappers without parsing each
// program's grammar. This deliberately overmatches arguments, but only deny
// decisions are used: xargs echo rm x can be denied by rm *.
var denyReachWrappers = map[string]bool{
	"command": true, "builtin": true, "exec": true, "nohup": true,
	"env": true, "nice": true, "ionice": true, "timeout": true,
	"gtimeout": true, "xargs": true, "sudo": true, "doas": true,
	"su": true, "stdbuf": true, "chrt": true, "taskset": true,
	"setsid": true, "flock": true, "chroot": true, "unbuffer": true,
	"caffeinate": true, "time": true, "strace": true, "ltrace": true,
	"nsenter": true, "unshare": true, "runuser": true, "firejail": true,
	"busybox": true,
}

var denyReachAssignment = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\[[^]]+\])?\+?=`)

// denyReachLeading removes shell prefixes that do not name the command.
// A subshell's "(" can be written against the command ("(rm -rf x)"), and
// is taken off the word as well as standing alone.
func denyReachLeading(words []shellWord) []shellWord {
	for len(words) > 0 {
		word := words[0].text
		if len(word) > 1 && word[0] == '(' {
			rest := append([]shellWord{{text: strings.TrimLeft(word, "("), expands: words[0].expands}}, words[1:]...)
			if rest[0].text == "" {
				rest = rest[1:]
			}
			words = rest
			continue
		}
		switch word {
		case "!", "{", "(", "then", "do", "else", "elif", "if", "while", "until", "coproc":
			words = words[1:]
		case "time":
			words = words[1:]
			if len(words) > 0 && words[0].text == "-p" {
				words = words[1:]
			}
		default:
			if !denyReachAssignment.MatchString(word) {
				return words
			}
			words = words[1:]
		}
	}
	return words
}

func denyReachText(words []shellWord) string {
	text := make([]string, len(words))
	for i, word := range words {
		text[i] = word.text
	}
	return strings.Join(text, " ")
}

// denyReaches checks commands behind shell prefixes, wrappers, command
// strings, find actions, and substitutions. It cannot grant permission.
func (c *Config) denyReaches(ctx context.Context, segment string, staticRequiresPermission bool) bool {
	depth, _ := ctx.Value(denyReachDepthKey{}).(int)
	if depth >= 4 {
		return false
	}
	ctx = context.WithValue(ctx, denyReachDepthKey{}, depth+1)
	lineDenied := func(line string) bool {
		for _, part := range shellSegments(line) {
			if c.resolveSegment(ctx, part, staticRequiresPermission) == DecisionDeny {
				return true
			}
		}
		return false
	}

	// denied checks one command text, and the same text without the ")"
	// that closes a subshell ("(rm x)", "( rm x )").
	denied := func(text string) bool {
		for _, t := range []string{text, strings.TrimRight(text, ") ")} {
			if t == segment || t == "" {
				continue
			}
			tctx := context.WithValue(ctx, shellSegmentKey{}, t)
			if c.resolveOneStrict(tctx, BashToolName, t, staticRequiresPermission) == DecisionDeny {
				return true
			}
		}
		return false
	}

	// Slices in this queue always get shorter. Deduplication also avoids
	// revisiting a nested wrapper's suffixes through every outer wrapper.
	type item struct {
		words []shellWord
		// stdinArgs marks a command xargs runs: it gets its arguments
		// from input, so "xargs rm" runs rm with some, and "rm *" has to
		// see one to match.
		stdinArgs bool
	}
	pending := []item{{words: shellWords(segment)}}
	seen := make(map[string]bool)
	for len(pending) > 0 {
		it := pending[0]
		words := denyReachLeading(it.words)
		pending = pending[1:]
		if len(words) == 0 {
			continue
		}
		candidate := denyReachText(words)
		if it.stdinArgs {
			candidate += " _"
		}
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		if denied(candidate) {
			return true
		}
		program := normalizeProgram(words[0].text)
		// The program by the name a rule gives it: "/bin/rm" and "RM" (on
		// a file system that ignores case) run rm, and "rm *" names rm.
		if program != words[0].text && denied(program+strings.TrimPrefix(candidate, words[0].text)) {
			return true
		}
		if denyReachWrappers[program] {
			for j := 1; j < len(words); j++ {
				pending = append(pending, item{words: words[j:], stdinArgs: program == "xargs"})
			}
		}
		// The word after an option, past a "--" that ends the options.
		after := func(j int) (string, bool) {
			if j+1 < len(words) && words[j+1].text == "--" {
				j++
			}
			if j+1 < len(words) {
				return words[j+1].text, true
			}
			return "", false
		}
		switch program {
		case "sh", "bash", "zsh", "dash", "ksh", "mksh", "ash", "su":
			for j := 1; j < len(words); j++ {
				option := words[j].text
				if strings.HasPrefix(option, "-") && !strings.HasPrefix(option, "--") && strings.Contains(option, "c") {
					if script, ok := after(j); ok && lineDenied(script) {
						return true
					}
				}
			}
		case "env":
			// -S splits one string into the command line it runs.
			for j := 1; j < len(words); j++ {
				w := words[j].text
				if rest, ok := strings.CutPrefix(w, "--split-string="); ok {
					if lineDenied(rest) {
						return true
					}
					continue
				}
				if w == "-S" || w == "--split-string" {
					if script, ok := after(j); ok && lineDenied(script) {
						return true
					}
					continue
				}
				if strings.HasPrefix(w, "-S") && len(w) > 2 && lineDenied(w[2:]) {
					return true
				}
			}
		case "eval":
			rest := words[1:]
			if len(rest) > 0 && rest[0].text == "--" {
				rest = rest[1:]
			}
			if lineDenied(denyReachText(rest)) {
				return true
			}
		case "watch":
			j := 1
			for j < len(words) && strings.HasPrefix(words[j].text, "-") {
				option := words[j].text
				j++
				if option == "--" {
					break
				}
				if (option == "-n" || option == "--interval") && j < len(words) {
					j++
				}
			}
			if lineDenied(denyReachText(words[j:])) {
				return true
			}
		case "find":
			for j := 1; j < len(words); j++ {
				switch words[j].text {
				case "-exec", "-execdir", "-ok", "-okdir":
					end := j + 1
					for end < len(words) && words[end].text != ";" && words[end].text != "+" {
						end++
					}
					pending = append(pending, item{words: words[j+1 : end]})
					j = end
				}
			}
		}
	}
	for _, body := range shellSubstitutionBodies(segment) {
		if lineDenied(body) {
			return true
		}
	}
	return false
}

// shellSubstitutionBodies returns balanced outer substitution bodies.
// Resolving a body visits its nested substitutions at the next deny depth.
// Each body has its own quote state: quotes outside $(...) do not quote
// its commands. Single quotes and escaped openers are literal. The stack
// keeps scanning independent of Go recursion.
//
// An outer body still open at the end is returned to the end of the text.
// The segment splitter cuts "echo $(rm -rf x; echo done)" at its ";", so
// the segment this sees is "echo $(rm -rf x", and the command is in the
// part that never closes. Only a deny is decided from a body.
func shellSubstitutionBodies(text string) []string {
	type frame struct {
		start  int
		close  byte
		quote  byte
		parens int
	}
	stack := []frame{{}}
	var bodies []string
	for i := 0; i < len(text); i++ {
		f := &stack[len(stack)-1]
		ch := text[i]
		if f.quote == '\'' {
			if ch == '\'' {
				f.quote = 0
			}
			continue
		}
		if ch == '\\' {
			// Inside double quotes, bash only escapes these characters.
			if i+1 < len(text) && (f.quote == 0 || strings.ContainsRune("$`\"\\\n", rune(text[i+1]))) {
				i++
			}
			continue
		}
		if len(stack) > 1 && ch == f.close && f.quote == 0 && f.parens == 0 {
			body := text[f.start:i]
			if f.close == '`' {
				// Bash removes these escapes before parsing a backtick
				// body. Escaped backticks can therefore nest commands.
				body = strings.NewReplacer("\\\\", "\\", "\\`", "`", "\\$", "$").Replace(body)
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 1 {
				bodies = append(bodies, body)
			}
			continue
		}
		if ch == '`' {
			stack = append(stack, frame{start: i + 1, close: '`'})
			continue
		}
		if i+1 < len(text) && text[i+1] == '(' && (ch == '$' || (f.quote == 0 && (ch == '<' || ch == '>'))) {
			stack = append(stack, frame{start: i + 2, close: ')'})
			i++
			continue
		}
		if ch == '"' {
			if f.quote == '"' {
				f.quote = 0
			} else {
				f.quote = '"'
			}
			continue
		}
		if f.quote != 0 {
			continue
		}
		if ch == '\'' {
			f.quote = ch
			continue
		}
		if len(stack) > 1 && f.close == ')' {
			if ch == '(' {
				f.parens++
			}
			if ch == ')' && f.parens > 0 {
				f.parens--
			}
		}
	}
	if len(stack) > 1 && stack[1].start <= len(text) {
		bodies = append(bodies, text[stack[1].start:])
	}
	return bodies
}
