package tools

import (
	"sort"
	"strings"
)

// Getting a tool's name slightly wrong.
//
// A model that asks for a tool nobody has is answered by name, and until
// this file existed that answer was a dead end: `tool "bash.command" is
// not available to this agent` names the mistake and nothing else — not
// what the tools actually are, not that "bash" is one of them, not what
// to do next. A model with no way to find out guesses again, identically,
// and the transcript this was found in holds five of the same call in a
// row before anyone stopped it.
//
// That is the same defect as grep's silent cap and edit's unexplained
// miss, in the same place: a tool result that reports a failure without
// reporting its cause is a turn the model cannot recover from. So this is
// fixed unconditionally rather than behind the Smart Agent switch, for the
// reason those two were: the answer was not merely less useful than it
// could be, it was insufficient to act on.
//
// Two things happen here. A name that is unambiguously a decorated form of
// a real one resolves to it and the call runs, because "bash.command" with
// {"command":"ls"} means bash and pretending otherwise is a fiction nobody
// benefits from. Everything else is refused with the roster attached.

// Nearest resolves a tool name against the names actually on offer.
//
// exact reports that the name needed no repair. When it is false and match
// is non-empty, the name was a decorated form of exactly one real tool.
// When match is empty, candidates holds whatever came close, which may be
// nothing at all.
//
// Resolution is against the caller's own list, never a registry. An agent
// restricted to three tools cannot reach a fourth by misspelling it,
// because the fourth is not in the set being searched.
func Nearest(available []string, asked string) (match string, exact bool, candidates []string) {
	asked = strings.TrimSpace(asked)
	if asked == "" {
		return "", false, nil
	}
	for _, n := range available {
		if n == asked {
			return n, true, nil
		}
	}

	// Every form of the asked name worth trying, cheapest first. Each is a
	// decoration models are observed to add, and each strips rather than
	// invents: nothing here can turn one real name into another.
	forms := []string{asked}
	if i := strings.Index(asked, "."); i > 0 {
		// "bash.command": a tool named by one of its own parameters.
		forms = append(forms, asked[:i])
	}
	if i := strings.LastIndex(asked, "."); i >= 0 && i < len(asked)-1 {
		// "functions.bash", "mcp.filesystem.read": a namespace in front.
		forms = append(forms, asked[i+1:])
	}

	found := map[string]bool{}
	for _, form := range forms {
		for _, n := range available {
			if fold(n) == fold(form) {
				found[n] = true
			}
		}
	}
	switch len(found) {
	case 0:
	case 1:
		for n := range found {
			return n, false, nil
		}
	default:
		return "", false, sortedNames(found)
	}

	// Nothing matched even loosely. Offer whatever shares a prefix, which
	// is what a truncated name looks like, and stop there: a general
	// edit-distance search over tool names produces confident nonsense,
	// and the roster is going in the message regardless.
	for _, n := range available {
		a, b := fold(n), fold(asked)
		if a == "" || b == "" {
			continue
		}
		if strings.HasPrefix(a, b) || strings.HasPrefix(b, a) {
			found[n] = true
		}
	}
	return "", false, sortedNames(found)
}

// fold reduces a name to what two spellings of it have in common:
// lowercase letters and digits. "read_file", "readFile", "read-file" and
// "ReadFile" are one name written four ways, and which one a model
// produces depends on how its training data spelled tools rather than on
// anything about this call.
func fold(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sortedNames(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// NoSuchTool is what a model is told when a name did not resolve.
//
// The roster is the point. A refusal that says only "no" leaves the model
// guessing at a list it cannot see, and guessing is what it does: the same
// call, again, until something else stops the turn.
//
// Capped, because an agent with sixty MCP tools would otherwise spend a
// large tool result on a list the model mostly does not need — but capped
// with the count said out loud, so "these are the tools" is never a claim
// this message makes falsely.
func NoSuchTool(available []string, asked string, candidates []string) string {
	var b strings.Builder
	b.WriteString("no tool is called " + quote(asked) + ".")
	switch {
	case len(candidates) == 1:
		b.WriteString(" Did you mean " + quote(candidates[0]) + "?")
	case len(candidates) > 1:
		b.WriteString(" Closest: " + strings.Join(quoteAll(candidates), ", ") + ".")
	}
	if len(available) == 0 {
		b.WriteString(" This agent has no tools at all, so no call will succeed; answer from what you already know.")
		return b.String()
	}
	names := append([]string(nil), available...)
	sort.Strings(names)
	const shown = 40
	if len(names) > shown {
		b.WriteString(" The " + itoa(len(names)) + " tools available to you begin: " +
			strings.Join(quoteAll(names[:shown]), ", ") + ".")
		return b.String()
	}
	b.WriteString(" The tools available to you are: " + strings.Join(quoteAll(names), ", ") + ".")
	return b.String()
}

func quote(s string) string { return `"` + s + `"` }

func quoteAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = quote(s)
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
