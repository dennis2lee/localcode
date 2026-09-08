package agent

import (
	"fmt"
	"sort"
	"strings"

	"localcode/internal/events"
)

// A message that looks like a command this build does not have.
//
// It is refused, and the reason it is refused rather than passed along is
// the report that produced this file: somebody typed a slash command,
// nothing here recognised it, and the text went to the model as an
// ordinary prompt. The model was in a session with a shell and skipped
// permissions, and it did what the word meant — every untracked file in
// the project was gone.
//
// That is the shape of the hazard, and "/clean" is only the sharpest
// example of it. A slash at the start of a message is somebody addressing
// the program, not the model. Handing it to a model instead turns a typo
// into an instruction, and the model has tools.
//
// So an unrecognised one is answered here, last in the table, once every
// route that could claim it has had its turn: the built-ins, the custom
// commands in .localcode/commands, and the skills.

// looksLikeCommand reports whether the first word of text is somebody
// addressing the program.
//
// The first word only, and it has to be a slash followed by a plain name:
// no second slash and no dot, because those are what a path looks like.
// "/etc/hosts is wrong" and "/tmp/x.log" are prose about a file and go to
// the model as they always have; "/clean" and "/clear-all" are not.
func looksLikeCommand(text string) (string, bool) {
	first := strings.TrimSpace(text)
	if i := strings.IndexAny(first, " \t\n"); i >= 0 {
		first = first[:i]
	}
	name, ok := strings.CutPrefix(first, "/")
	if !ok || name == "" {
		return "", false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9', r == '-', r == '_':
			if i == 0 {
				return "", false
			}
		default:
			// A slash, a dot, anything else: a path or prose.
			return "", false
		}
	}
	return name, true
}

// routeUnknownCommand answers a slash command nothing else claimed.
func (l *Loop) routeUnknownCommand(sessionID, text string) (bool, error) {
	name, ok := looksLikeCommand(text)
	if !ok {
		return false, nil
	}
	l.Store.Append(sessionID, events.TypeUserMessage, map[string]any{"text": text, "local": true})

	known := l.knownCommandNames()
	var b strings.Builder
	fmt.Fprintf(&b, "There is no /%s in this build, so nothing was run.\n\n", name)
	// Named rather than swallowed, because the reason this is refused at
	// all is that the alternative was a model acting on it.
	b.WriteString("A message beginning with a slash is read as a command. It is not sent to the model, " +
		"which is the point: a command this build does not have would otherwise arrive as an instruction, " +
		"and the model has a shell.\n\n")
	if suggestion := nearestCommand(known, name); suggestion != "" {
		fmt.Fprintf(&b, "Did you mean /%s?\n\n", suggestion)
	}
	b.WriteString("Commands: " + strings.Join(withSlashes(known), ", ") + "\n\n")
	b.WriteString("To say this to the model instead, put a word in front of it or wrap it in backticks.")
	return true, l.replyText(sessionID, b.String())
}

// nearestCommand is the one command a mistyped name probably meant, or
// "" when there is not exactly one.
//
// One edit away, and only one candidate at that distance. tools.Nearest
// deliberately refuses an edit-distance search, because it answers a
// model that invented a tool name and a confident wrong guess there
// becomes a call. This answers a person who mistyped, who reads the
// suggestion before acting on it — and the case that matters is exactly
// one edit wide: "/clean" for "/clear" is the report this file exists
// for. Two candidates equally close are not a suggestion, they are a
// coin toss, so nothing is offered and the list below stands on its own.
func nearestCommand(known []string, asked string) string {
	asked = strings.ToLower(asked)
	var best string
	count := 0
	for _, n := range known {
		if editDistanceWithin(strings.ToLower(n), asked, 1) {
			best = n
			count++
		}
	}
	if count == 1 {
		return best
	}
	return ""
}

// editDistanceWithin reports whether a and b are at most max edits apart,
// counting a substitution, an insertion or a deletion. Written for max 1,
// which is all this needs and is what keeps it a comparison rather than a
// guess.
func editDistanceWithin(a, b string, max int) bool {
	if a == b {
		return true
	}
	if max < 1 {
		return false
	}
	la, lb := len(a), len(b)
	if la > lb {
		a, b, la, lb = b, a, lb, la
	}
	if lb-la > max {
		return false
	}
	// One pass: walk together, allow a single divergence.
	for i := 0; i < la; i++ {
		if a[i] == b[i] {
			continue
		}
		if la == lb {
			// A substitution: the rest has to match exactly.
			return a[i+1:] == b[i+1:]
		}
		// An insertion in the longer one.
		return a[i:] == b[i+1:]
	}
	// The shorter is a prefix of the longer, one character short.
	return lb-la <= max
}

// knownCommandNames is every name a slash could resolve to here: the
// commands the daemon answers, the custom commands, and the skills.
//
// The terminal's own commands are not in it. They never reach this — the
// TUI answers them before the daemon sees them — and naming them in a
// list the Web UI also reads would be offering something that client
// does not have.
func (l *Loop) knownCommandNames() []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if n = strings.TrimSpace(n); n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, c := range SlashCommands() {
		add(c.Name)
	}
	for _, c := range l.Commands {
		add(c.Name)
	}
	for _, s := range l.SkillList() {
		add(s.Name)
	}
	sort.Strings(out)
	return out
}

func withSlashes(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = "/" + n
	}
	return out
}
