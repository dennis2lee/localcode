package agent

import (
	"strings"

	"localcode/internal/provider"
)

// Per-model notes appended to the system prompt.
//
// A model's habits are not always a property of the conversation, and some
// of them are wrong here specifically. Gemma writes mathematical and
// arrow-ish text as LaTeX — `$\rightarrow$`, `$\text{name}$` — which is
// correct in a chat client that renders MathJax and is literal noise in
// one that does not. This window renders Markdown and nothing else, so
// the text arrives with the dollars and the backslashes in it.
//
// The alternative to a note like this is a renderer that understands a
// subset of LaTeX, which is a larger thing to own and still leaves the
// model spending tokens on markup nobody wanted. Asking the model not to
// is cheaper, and it costs nothing on the models that never do it —
// which is why it is keyed on the model rather than sent to everyone.
//
// Matched on the model id, lowercased, as a substring: ids are vendor
// strings with sizes and quantisations in them ("gemma-3-27b-it-q4"), and
// what identifies the family is the family name.
var modelQuirks = []struct {
	match string
	note  string
	// keepGoing is the default carry-on budget for this family — the
	// number of times one turn may be told to continue after the model
	// stops with the task unfinished. It exists so the fix ships in the
	// release rather than in a config key: the person who reported the
	// stall should get a model that finishes by installing the update,
	// not by learning a setting. A profile's own keep_going overrides it,
	// and -1 turns it off. See keep_going.go.
	keepGoing int
}{
	{
		match: "gemma",
		note: "Output formatting: this interface renders Markdown only — there is no LaTeX or MathJax in it. " +
			"Never wrap text in $...$, $$...$$, \\(...\\) or \\[...\\], and do not use LaTeX commands such as " +
			"\\rightarrow, \\text{...}, \\mathbf{...}, \\times or \\le: they are shown to the user exactly as " +
			"written, dollars and backslashes included. Write the character itself (-> or →, ×, ≤), plain words, " +
			"or a fenced code block, and use **bold** rather than \\mathbf for emphasis. Ordinary Markdown — " +
			"headings, lists, tables, **bold**, `code` — renders correctly and is welcome.",
	},
	{
		// Reported against muse-glimmer-30b, with the transcript: a build
		// fails, the model reads the error, writes "global_init.cpp also
		// has to be updated" — and ends its turn. "진행" makes it pick up,
		// and it stops again one step later.
		//
		// Matched on "muse", not "glimmer", and the difference was a
		// live bug: the habit is the family's, every muse variant has
		// it, and a variant without "glimmer" in its id got a carry-on
		// budget of zero — the feature built for these models was off
		// for most of them. The family name is the part every variant
		// shares.
		//
		// A note is not a guarantee, which is why keep_going exists as
		// well (see keep_going.go). It costs one paragraph on a model
		// that has the habit and is sent to nothing else.
		match:     "muse",
		keepGoing: 3,
		note: "Working style: finish the task before you end your turn. When a tool result shows you what has " +
			"to happen next — another file to update, a build to re-run, a test to fix — do it in the same turn " +
			"rather than describing it and stopping. Writing 'X also needs updating' and ending the turn leaves " +
			"the user to type 'continue', which is work you were asked to do. End your turn when the task is " +
			"complete, or when you need a decision only the user can make; in the second case, say plainly what " +
			"you need.",
	},
}

// museModel reports whether model is one of the muse family, by the same
// substring rule every other muse decision here uses.
func museModel(model string) bool { return strings.Contains(strings.ToLower(model), "muse") }

// museReasoningLine is how an effort level reaches a muse model.
//
// Muse does not read reasoning_effort. Its model card sets the amount of
// reasoning through the system prompt, as "Reasoning strength: <low |
// medium | high | xhigh>", and asks for high or xhigh on coding and
// agentic work. Until this existed, "/effort high" on a muse profile
// changed a request field the model ignores, and every muse conversation
// ran at whatever the server's default strength is, which the publisher
// says is the wrong one for code.
//
// Only when a level is set. Unset and off send nothing here for the same
// reason they send nothing on the wire: the model's own default is a
// setting too, and the person who has not asked keeps it.
func museReasoningLine(model string, level provider.Effort) string {
	if !museModel(model) {
		return ""
	}
	switch level {
	case provider.EffortLow, provider.EffortMedium, provider.EffortHigh, provider.EffortXHigh:
		return "Reasoning strength: " + string(level)
	}
	return ""
}

// quirkNote returns the addition for a model, or "" for a model with no
// known quirk.
func quirkNote(model string) string {
	id := strings.ToLower(model)
	for _, q := range modelQuirks {
		if strings.Contains(id, q.match) {
			return q.note
		}
	}
	return ""
}

// modelNoteFor is the per-model system text: the family's quirk note, and
// for muse the reasoning line the effort level asks for. On the same
// asset because they are the same kind of thing, words this model needs
// that no other does, and because a line that changes with /effort has
// to be re-derived per turn, which the quirk asset already is.
func modelNoteFor(model string, level provider.Effort) string {
	note := quirkNote(model)
	line := museReasoningLine(model, level)
	switch {
	case note == "":
		return line
	case line == "":
		return note
	}
	return line + "\n\n" + note
}

// modelFamily names the family a model id belongs to, using the same
// matching the quirk notes use, so "the note written for this family" and
// "this family" cannot drift apart.
//
// A family rather than the full id because that is the granularity the
// per-model prompts are written at, and because it is what a manifest
// wants to record: two requests to different snapshots of the same family
// got the same prompt, and saying so is more useful than printing two
// version strings that differ in a date.
func modelFamily(model string) string {
	id := strings.ToLower(model)
	for _, q := range modelQuirks {
		if strings.Contains(id, q.match) {
			return q.match
		}
	}
	return "default"
}

// modelKeepGoing is the default carry-on budget for a model, keyed the
// same way as the notes. Zero for a model with no known stalling habit.
func modelKeepGoing(model string) int {
	id := strings.ToLower(model)
	for _, q := range modelQuirks {
		if strings.Contains(id, q.match) {
			return q.keepGoing
		}
	}
	return 0
}
