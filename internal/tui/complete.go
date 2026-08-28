package tui

import "strings"

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
// skills and the custom commands.
//
// Both, because both are invoked the same way and someone typing "/re"
// is not thinking about which of the two lists "review" is in. Built-in
// commands are deliberately absent: there are twenty of them, they are
// in /help, and cycling through /compact on the way to a skill is a
// worse experience than typing the four characters.
func (m Model) completionCandidates() []string {
	out := make([]string, 0, len(m.skillsList)+len(m.commandsList))
	for _, s := range m.skillsList {
		out = append(out, "/"+s.Name)
	}
	for _, c := range m.commandsList {
		out = append(out, "/"+c.Name)
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

// nextCompletion advances the walk and returns what the box should say.
//
// The candidate list has the typed text appended to it, so the last step
// of the cycle returns to where it started. ok is false when there is
// nothing to offer, which leaves the key to do whatever it did before.
func (m *Model) nextCompletion(text string) (string, bool) {
	prefix, ok := completionPrefix(text)
	if !ok {
		m.completion = completionState{}
		return "", false
	}
	// A walk continues only if the box still holds what the walk last
	// put there. Anything else is a new prompt and starts over.
	if m.completion.last != text || m.completion.prefix == "" {
		// Before the first candidate, not on it: the walk advances and
		// then reads, so a fresh walk has to start one step back or its
		// first press skips to the second name.
		m.completion = completionState{prefix: prefix, idx: -1}
	}
	matches := completionsFor(m.completionCandidates(), m.completion.prefix)
	if len(matches) == 0 {
		return "", false
	}
	// The typed text closes the ring.
	ring := append(append([]string{}, matches...), m.completion.prefix)
	m.completion.idx = (m.completion.idx + 1) % len(ring)
	next := ring[m.completion.idx]
	m.completion.last = next
	return next, true
}

// completionHint is what the footer says while a walk is available: how
// many candidates there are, so an ambiguous prefix looks ambiguous
// before you press anything.
func (m Model) completionHint() string {
	prefix, ok := completionPrefix(strings.TrimSpace(m.input.Value()))
	if !ok {
		return ""
	}
	// While walking, the count is of the walk's own prefix; the box
	// holds a completion, not what was typed.
	if m.completion.last == prefix && m.completion.prefix != "" {
		prefix = m.completion.prefix
	}
	matches := completionsFor(m.completionCandidates(), prefix)
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
