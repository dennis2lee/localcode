package agent

import (
	"fmt"
	"regexp"
	"strings"

	"localcode/internal/provider"
	"localcode/internal/session"
)

// Referring to another conversation with "#<name>".
//
// What this file does is resolve the name. What it deliberately does not
// do is fetch anything: not one byte of the named conversation enters the
// message. A reference becomes a line of localcode's own text naming the
// conversation, and its contents arrive only if the model asks for them
// through the session_read tool.
//
// That split is the whole design, and it is not a matter of taste. Splicing
// another conversation's transcript into a user-role message would put a
// mixture of somebody else's typing, a model's own words, fetched pages and
// MCP output where the only thing holding it down is a label, and this
// repository says twice that a trust label is a declaration rather than a
// boundary. As a tool result the same text is held down by the shape of the
// code instead: it can reach history only as a tool_result block, which
// historyEntries routes to toolResultEntry, which hard-codes TrustExternal
// with no argument a caller could pass; the tool is filtered by name in the
// registry, so an agent whose config omits it cannot call it; capToolResult
// bounds it before it is stored or sent; and, decisively, a tool result
// never re-enters SendMessage, so a "#S3" inside the referenced transcript
// cannot resolve and a "/permission-skip-all on" inside it cannot reach the
// command router that once executed exactly that.
//
// Resolution never refuses. Every outcome, including no match at all, is a
// notice, and the turn goes on. That is affordable only because a reference
// produces metadata rather than content: in a splice design a wrong
// resolution silently changes what the model was given, so it would have to
// refuse, and refusing would make "#include" in a pasted C snippet a failed
// turn.

// sessionRefPattern matches a reference.
//
// Two forms, because a session title routinely contains spaces and the
// "@path" trick of taking everything up to whitespace works for paths only
// because paths do not. A quoted name may hold anything but a quote or a
// newline.
//
// What it deliberately does not match: "# " (a markdown heading is a hash
// followed by a space) and a bare "#" at the end of a line. A digits-only
// token is rejected after matching rather than in the pattern, so "issue
// #42" is inert while a conversation genuinely titled "42" is still
// reachable by id.
var sessionRefPattern = regexp.MustCompile(`#"[^"\n]*"|#[^\s"#]+`)

// maxSessionRefs bounds how many references one message resolves.
//
// A bound rather than none, because each one costs a line in every later
// re-send of the history, and because a message naming twelve
// conversations is not a reference, it is a paste. The sixth is not
// dropped in silence: the notice says how many were skipped.
const maxSessionRefs = 5

// maxRefTitle caps a title inside a notice, and the strip beside it is the
// part that matters.
//
// The notice is localcode's own text and carries TrustSystem, so it may
// instruct. Its only variable content is session metadata, and the one
// field a person can write is the title, which nothing validates: SetTitle
// and the rename endpoint both accept anything, and a model holding bash
// can reach that endpoint. Without the strip a title could carry newlines
// and forge extra lines of a notice the model is entitled to follow.
const maxRefTitle = 80

// sessionRef is one resolved reference.
type sessionRef struct {
	// Text is the token as typed, so a notice can quote it back.
	Text string
	// Name is the token with its "#" and any quotes removed.
	Name string
	// Match is the conversation it resolved to, or nil.
	Match *session.Session
	// Candidates is filled when the name was ambiguous, so the notice can
	// list them rather than pick one.
	Candidates []session.Session
	// Self reports that the reference names the conversation it was typed
	// in, whose history the model already has.
	Self bool
}

// findSessionRefs returns the references in a typed message, in order,
// and how many were past the limit.
//
// Over the typed text only. Nothing spliced is rescanned, which is what
// makes references non-transitive by construction rather than by a check.
func findSessionRefs(text string) (names []string, skipped int) {
	for _, tok := range sessionRefPattern.FindAllString(text, -1) {
		name := strings.TrimPrefix(tok, "#")
		name = strings.Trim(name, `"`)
		if name == "" || isAllDigits(name) {
			// "#42" is an issue number in every project that has ever
			// written one down. A conversation titled 42 is still
			// reachable by its id.
			continue
		}
		if len(names) >= maxSessionRefs {
			skipped++
			continue
		}
		names = append(names, tok)
	}
	return names, skipped
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// resolveSessionRefs resolves every reference in text against the
// conversations this daemon holds.
//
// The search space is the visible conversations plus the archived ones.
// Background-task and scheduled-run sessions are in neither and are
// unreachable here by design: a session with a parent is a task, addressed
// through the tasks API, which is the division handleTaskOutput already
// made from the other side.
//
// Archived conversations resolve normally. Archiving refuses starting work
// and never refuses reading.
func (l *Loop) resolveSessionRefs(sessionID, text string) ([]sessionRef, int) {
	tokens, skipped := findSessionRefs(text)
	if len(tokens) == 0 || l.Store == nil {
		return nil, skipped
	}

	pool := append(l.Store.ListVisible(), l.Store.ListArchived()...)
	out := make([]sessionRef, 0, len(tokens))
	seen := map[string]bool{}
	for _, tok := range tokens {
		name := strings.Trim(strings.TrimPrefix(tok, "#"), `"`)
		if seen[strings.ToLower(name)] {
			// The same conversation named twice in one message is one
			// reference: two identical notices say nothing the first did
			// not.
			continue
		}
		seen[strings.ToLower(name)] = true

		ref := sessionRef{Text: tok, Name: name}
		if strings.EqualFold(name, sessionID) {
			ref.Self = true
			out = append(out, ref)
			continue
		}
		match, candidates := matchSession(pool, name)
		if match != nil && match.ID == sessionID {
			ref.Self = true
		}
		ref.Match, ref.Candidates = match, candidates
		out = append(out, ref)
	}
	return out, skipped
}

// matchSession resolves one name, or reports the candidates it could not
// choose between.
//
// The order is exact id, exact title, then unique title prefix. Never the
// newest of several: duplicate titles are reachable on any existing store,
// since nothing validates a title and forking one conversation twice
// produces two called "fork of X", so picking one silently would be
// picking wrong half the time.
func matchSession(pool []session.Session, name string) (*session.Session, []session.Session) {
	for i := range pool {
		if pool[i].ID == name {
			return &pool[i], nil
		}
	}

	var exact, prefix []session.Session
	lower := strings.ToLower(name)
	for _, s := range pool {
		title := strings.ToLower(strings.TrimSpace(s.Title))
		if title == "" {
			continue
		}
		switch {
		case title == lower:
			exact = append(exact, s)
		case strings.HasPrefix(title, lower):
			prefix = append(prefix, s)
		}
	}
	if len(exact) == 1 {
		return &exact[0], nil
	}
	if len(exact) > 1 {
		return nil, exact
	}
	if len(prefix) == 1 {
		return &prefix[0], nil
	}
	if len(prefix) > 1 {
		return nil, prefix
	}
	return nil, nil
}

// sessionRefNotice is what the model is told about one reference: enough
// to decide whether to read it, and nothing out of it.
func sessionRefNotice(ref sessionRef, thisWorkspace string) string {
	switch {
	case ref.Self:
		return fmt.Sprintf("%s names this conversation, whose history you already have. "+
			"There is nothing to read.", ref.Text)

	case len(ref.Candidates) > 0:
		var b strings.Builder
		fmt.Fprintf(&b, "%s names %d conversations. Read one by id with session_read, or ask which was meant:",
			ref.Text, len(ref.Candidates))
		for _, c := range ref.Candidates {
			fmt.Fprintf(&b, "\n  * %s (%s)%s", safeTitle(c.Title), c.ID, archivedNote(c))
			if c.Workspace != "" {
				fmt.Fprintf(&b, ", working in %s", c.Workspace)
			}
		}
		return b.String()

	case ref.Match == nil:
		return fmt.Sprintf("%s names no conversation on this daemon. Do not guess at what it "+
			"referred to; say so.", ref.Text)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s is the conversation %s (id %s)%s.",
		ref.Text, safeTitle(ref.Match.Title), ref.Match.ID, archivedNote(*ref.Match))
	if ref.Match.Workspace != "" {
		fmt.Fprintf(&b, " It works in %s", ref.Match.Workspace)
		if thisWorkspace != "" && ref.Match.Workspace != thisWorkspace {
			// The line this feature most needs. A referenced transcript is
			// full of paths relative to its own directory, and following
			// one from here writes into another project: the defect fixed
			// in v0.75.1, arriving by a different route.
			fmt.Fprintf(&b, ", which is NOT this conversation's directory (%s). "+
				"Any path you read there is relative to that project, not this one; "+
				"do not open one here without checking it exists here", thisWorkspace)
		}
		b.WriteString(".")
	}
	b.WriteString(" Read it with the session_read tool; nothing of it is in this message.")
	return b.String()
}

func archivedNote(s session.Session) string {
	if s.ArchivedAt != nil {
		return ", archived"
	}
	return ""
}

// safeTitle renders a title inside product text.
//
// Stripped and capped rather than quoted, because the notice carries
// TrustSystem and a title is the one field in it a person writes. Nothing
// validates a title anywhere, and a model with bash can reach the rename
// endpoint, so a newline here is a way to forge a line the model is
// entitled to follow.
func safeTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "(untitled)"
	}
	var b strings.Builder
	for _, r := range title {
		if r == '\n' || r == '\r' || r == '\t' || r < 0x20 || r == 0x7f {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	// "[localcode:" is the marker every line of a notice opens with, and a
	// notice may instruct. Stripping newlines already stops a title forging
	// a whole line; this stops it forging the marker inside one, which a
	// model scanning for the prefix would find in a quoted title.
	out = strings.ReplaceAll(out, "[localcode:", "[localcode -")
	if len(out) > maxRefTitle {
		out = out[:maxRefTitle] + "..."
	}
	return `"` + out + `"`
}

// expandSessionRefs returns the model's copy of a message, with a notice
// appended for each reference, plus the notices the person is shown.
//
// The typed text is never edited: "#S2" stays where it was, and the
// notices are appended after it. Editing in place would make the message
// the model reads differ from the one in the transcript at the point the
// eye is drawn to, and would put product text inside a sentence somebody
// wrote.
func (l *Loop) expandSessionRefs(sessionID, text string) (string, []provider.BlockSource, []string) {
	refs, skipped := l.resolveSessionRefs(sessionID, text)
	if len(refs) == 0 && skipped == 0 {
		return text, nil, nil
	}

	here := l.SessionDir(sessionID)
	var forModel []string
	var forPerson []string
	for _, ref := range refs {
		forModel = append(forModel, sessionRefNotice(ref, here))
		forPerson = append(forPerson, sessionRefPersonNotice(ref))
	}
	if skipped > 0 {
		// Said rather than dropped. A message naming eight conversations
		// that silently resolved five is one whose answer is wrong for a
		// reason nothing on screen explains.
		note := fmt.Sprintf("%d further reference(s) in this message were not resolved: "+
			"at most %d are, per message.", skipped, maxSessionRefs)
		forModel = append(forModel, note)
		forPerson = append(forPerson, note)
	}
	if len(forModel) == 0 {
		return text, nil, nil
	}
	// The span, so the notice is localcode's own text on the manifest
	// rather than part of what the person typed. It is the one piece of a
	// reference that may instruct, and the trust boundary asset tells the
	// model to weigh product text differently from a tool result, so a
	// notice folded anonymously into a user message would be exactly the
	// promotion the inventory exists to make visible.
	out := text + "\n\n[localcode: " + strings.Join(forModel, "\n[localcode: ") + "]"
	span := provider.BlockSource{ID: referenceNoticeID, From: len(text), To: len(out)}
	return out, []provider.BlockSource{span}, forPerson
}

// sessionRefPersonNotice is the same fact, for the person who typed it.
//
// Shorter than the model's, because it is a confirmation rather than an
// instruction: what they need is whether the name landed where they meant.
func sessionRefPersonNotice(ref sessionRef) string {
	switch {
	case ref.Self:
		return ref.Text + " is this conversation"
	case len(ref.Candidates) > 0:
		names := make([]string, 0, len(ref.Candidates))
		for _, c := range ref.Candidates {
			names = append(names, safeTitle(c.Title)+" ("+c.ID+")")
		}
		return ref.Text + " matches " + fmt.Sprint(len(ref.Candidates)) + " conversations: " +
			strings.Join(names, ", ")
	case ref.Match == nil:
		return ref.Text + " matches no conversation"
	}
	out := ref.Text + " -> " + safeTitle(ref.Match.Title) + archivedNote(*ref.Match)
	if ref.Match.Workspace != "" {
		out += ", in " + ref.Match.Workspace
	}
	return out
}
