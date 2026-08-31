package tui

import (
	"regexp"
	"strings"
)

// Completing "/<name>" is a small feature with one detail that decides
// whether it is useful: what happens when the prefix is ambiguous.
//
// Offering the single common prefix, the way a shell does, is the wrong
// answer here. Skills are named for what they do rather than to share
// prefixes, so the common prefix of "pdf-tools" and "pptx" is "p" and
// completing to it has told you nothing. Offering the first match and
// stopping is worse: it is confidently wrong whenever the one you want
// is second.
//
// So the same key walks the candidates. The first press completes to
// one, the next replaces it with the following one, and the walk comes
// back round to what you typed. That last part matters: a cycle you
// cannot leave is a trap, and the text you typed is a candidate like the
// others.

// completionState remembers a walk in progress: the text the walk
// started from, and how far through the candidates it has gone.
//
// Keyed on the text the last completion produced, so any edit ends the
// walk by construction rather than by every editing path having to
// remember to clear it. Typing a character makes the box no longer match
// what completion last put there, and a walk that does not recognise the
// box is over.
type completionState struct {
	prefix string
	last   string
	// idx is the position in the ring the last press landed on, and -1
	// before the first press of a walk.
	idx int
}

// completionCandidates is everything "/" can be completed to: the
// skills, the custom commands, the commands the daemon answers, and the
// ones this client answers itself.
//
// All four, because they are all invoked the same way and somebody
// typing "/sm" is not thinking about which list "/smart-agent" is in.
// An earlier version left the built-in commands out on the grounds that
// walking past "/compact" on the way to a skill costs presses, which is
// true and is the smaller cost: a command you cannot complete is one you
// have to remember exactly, and "/permission-skip-all" is not a name
// anybody types twice from memory. The count in the footer is what makes
// a long walk bearable.
//
// Skills and custom commands come first because they are the ones a
// person installed, and the built-ins are the ones documented in /help.
func (m Model) completionCandidates() []string {
	out := make([]string, 0, len(m.skillsList)+len(m.commandsList)+len(m.slashList)+8)
	for _, s := range m.skillsList {
		out = append(out, "/"+s.Name)
	}
	for _, c := range m.commandsList {
		out = append(out, "/"+c.Name)
	}
	for _, c := range m.slashList {
		out = append(out, "/"+c.Name)
	}
	// This client's own, which the daemon has never heard of. Taken from
	// the table that dispatches them, so a command added there is
	// completable without a second list to remember.
	for _, c := range localCommands() {
		out = append(out, c.name)
	}
	return dedupe(out)
}

// dedupe keeps the first spelling of each name. A skill and a custom
// command can share one, and offering it twice in a walk looks like the
// key stopped working.
func dedupe(names []string) []string {
	seen := make(map[string]bool, len(names))
	out := names[:0]
	for _, n := range names {
		if seen[strings.ToLower(n)] {
			continue
		}
		seen[strings.ToLower(n)] = true
		out = append(out, n)
	}
	return out
}

// completionsFor returns every candidate starting with prefix, in the
// order the daemon listed them, which is alphabetical. Matching is
// case-insensitive because the names are, and because someone typing
// "/PDF" means the same thing.
func completionsFor(candidates []string, prefix string) []string {
	var out []string
	for _, c := range candidates {
		if strings.HasPrefix(strings.ToLower(c), strings.ToLower(prefix)) && !strings.EqualFold(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// completionPrefix reports the text to complete, and whether it is
// completable at all.
//
// A completable prompt is one word beginning with "/" and nothing else:
// "/pd" completes, "/pdf-tools split this" does not, because the
// completion is over. The cursor has to be at the end for the same
// reason Up only recalls history at the top of the box: the key means
// something else in the middle of a line, and taking it there would cost
// the ordinary use to serve the special one.
func completionPrefix(text string) (string, bool) {
	if !strings.HasPrefix(text, "/") || len(text) < 2 {
		return "", false
	}
	if strings.ContainsAny(text, " \t\n") {
		return "", false
	}
	return text, true
}

// sessionCandidates is everything "#" can be completed to: the other
// conversations on this daemon, by title where they have one and by id
// where they do not.
//
// The archived ones are in the list, and are close to the reason the list
// exists: the conversation you put away last month is exactly the one
// whose name you cannot remember. Referring to one is reading, and
// archiving only ever refuses starting work.
//
// This conversation is not in it. A reference to the conversation it was
// typed in resolves to "there is nothing to read", so offering it is
// offering a mistake.
func (m Model) sessionCandidates() []string {
	out := make([]string, 0, len(m.refNames))
	for _, s := range m.refNames {
		if s.ID == m.sessionID {
			continue
		}
		name := strings.TrimSpace(s.Title)
		if name == "" {
			name = s.ID
		}
		// Quoted when it has to be. A title routinely has spaces in it and
		// the daemon's grammar reads an unquoted name up to the first one,
		// so completing to a bare multi-word title would produce something
		// that does not parse.
		if strings.ContainsAny(name, " \t") {
			out = append(out, `#"`+name+`"`)
			continue
		}
		out = append(out, "#"+name)
	}
	return dedupe(out)
}

// bareName strips a token to the part being typed, so "#S2", `#"S2"` and
// "s2" all compare the same.
func bareName(tok string) string {
	return strings.ToLower(strings.Trim(strings.TrimPrefix(tok, "#"), `"`))
}

func sessionCompletionsFor(candidates []string, prefix string) []string {
	want := bareName(prefix)
	var out []string
	for _, c := range candidates {
		name := bareName(c)
		if strings.HasPrefix(name, want) && name != want {
			out = append(out, c)
		}
	}
	return out
}

var allDigits = regexp.MustCompile(`^[0-9]+$`)

// completionTarget finds what the cursor is sitting in, and is the whole
// difference between the two completions.
//
// A command is the first word of the box and nothing else, so it is
// matched against the whole text. A reference is not: "check #S2 against
// the file here" is the shape the feature exists for, so its token has to
// be found where the cursor is and spliced back in place rather than
// replacing the box.
//
// start and end bound the runes a completion replaces. Runes, not bytes,
// because a Korean prompt with a reference in it is the ordinary case
// here and a byte offset into one lands mid-character.
type completionTarget struct {
	session    bool
	start, end int
	prefix     string
}

func targetAt(text string, cursor int) (completionTarget, bool) {
	runes := []rune(text)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}
	if prefix, ok := completionPrefix(text); ok && cursor == len(runes) {
		return completionTarget{start: 0, end: len(runes), prefix: prefix}, true
	}

	// The nearest "#" at or before the cursor that opens a token: at the
	// start of the box, or with whitespace in front of it. Anything else
	// is a fragment identifier or somebody's C include.
	//
	// The scan does not stop at whitespace, because a quoted name is
	// allowed to contain some — `#"the parser` is a name half typed, and a
	// scan that gave up at the space would make every multi-word title
	// uncompletable, which is most of them. The token decides it instead.
	for i := cursor - 1; i >= 0; i-- {
		if runes[i] != '#' {
			continue
		}
		if i > 0 && !isSpace(runes[i-1]) {
			return completionTarget{}, false
		}
		token := string(runes[i:cursor])
		name := strings.TrimPrefix(token, "#")
		if strings.HasPrefix(name, `"`) {
			// A closing quote ends the name, so anything after it means
			// the cursor has left the reference. The quote itself is fine
			// to sit just after: that is where a completed name leaves the
			// cursor, and the walk has to carry on from there or pressing
			// the key twice offers the first candidate twice.
			rest := name[1:]
			if q := strings.Index(rest, `"`); q >= 0 && q != len(rest)-1 {
				return completionTarget{}, false
			}
		} else if strings.ContainsAny(token, " \t\n") {
			return completionTarget{}, false
		}
		// "#42" is an issue number, which is the daemon's rule too. A
		// completion offering something the grammar then ignores would be
		// the two halves disagreeing about what a reference is.
		if allDigits.MatchString(name) {
			return completionTarget{}, false
		}
		return completionTarget{session: true, start: i, end: cursor, prefix: token}, true
	}
	return completionTarget{}, false
}

func isSpace(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }

// nextCompletion advances the walk and returns what the box should say.
//
// The candidate list has the typed text appended to it, so the last step
// of the cycle returns to where it started. ok is false when there is
// nothing to offer, which leaves the key to do whatever it did before.
func (m *Model) nextCompletion(text string, cursor int) (string, int, bool) {
	target, ok := targetAt(text, cursor)
	if !ok {
		m.completion = completionState{}
		return "", 0, false
	}
	// A walk continues only if the box still holds what the walk last
	// put there. Anything else is a new prompt and starts over.
	if m.completion.last != text || m.completion.prefix == "" {
		// Before the first candidate, not on it: the walk advances and
		// then reads, so a fresh walk has to start one step back or its
		// first press skips to the second name.
		m.completion = completionState{prefix: target.prefix, idx: -1}
	}
	var matches []string
	if target.session {
		matches = sessionCompletionsFor(m.sessionCandidates(), m.completion.prefix)
	} else {
		matches = completionsFor(m.completionCandidates(), m.completion.prefix)
	}
	if len(matches) == 0 {
		return "", 0, false
	}
	// The typed text closes the ring.
	ring := append(append([]string{}, matches...), m.completion.prefix)
	m.completion.idx = (m.completion.idx + 1) % len(ring)
	chosen := ring[m.completion.idx]
	// Spliced, not substituted. For a command the span is the whole box
	// and the two are the same thing; for a reference they are not, and
	// replacing the box would delete the sentence the reference is in.
	runes := []rune(text)
	next := string(runes[:target.start]) + chosen + string(runes[target.end:])
	m.completion.last = next
	return next, target.start + len([]rune(chosen)), true
}

// completionHint is what the footer says while a walk is available: how
// many candidates there are, so an ambiguous prefix looks ambiguous
// before you press anything.
func (m Model) completionHint() string {
	text := m.input.Value()
	target, ok := targetAt(text, m.cursorRune())
	if !ok {
		return ""
	}
	// While walking, the count is of the walk's own prefix; the box
	// holds a completion, not what was typed.
	prefix := target.prefix
	if m.completion.last == text && m.completion.prefix != "" {
		prefix = m.completion.prefix
	}
	var matches []string
	if target.session {
		matches = sessionCompletionsFor(m.sessionCandidates(), prefix)
	} else {
		matches = completionsFor(m.completionCandidates(), prefix)
	}
	if len(matches) == 0 {
		return ""
	}
	if len(matches) == 1 {
		return "→ " + matches[0]
	}
	return "→ " + matches[0] + "  (" + itoa(len(matches)) + " matches, → cycles)"
}

// itoa keeps the hint free of a fmt import for one number.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
