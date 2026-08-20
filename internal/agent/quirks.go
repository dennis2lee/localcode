package agent

import "strings"

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
		// A note is not a guarantee, which is why keep_going exists as
		// well (see keep_going.go). It costs one paragraph on a model that
		// has the habit and is sent to nothing else.
		match:     "glimmer",
		keepGoing: 3,
		note: "Working style: finish the task before you end your turn. When a tool result shows you what has " +
			"to happen next — another file to update, a build to re-run, a test to fix — do it in the same turn " +
			"rather than describing it and stopping. Writing 'X also needs updating' and ending the turn leaves " +
			"the user to type 'continue', which is work you were asked to do. End your turn when the task is " +
			"complete, or when you need a decision only the user can make; in the second case, say plainly what " +
			"you need.",
	},
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
