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
}{
	{
		match: "gemma",
		note: "Output formatting: this interface renders Markdown only — there is no LaTeX or MathJax in it. " +
			"Never wrap text in $...$, $$...$$, \\(...\\) or \\[...\\], and do not use LaTeX commands such as " +
			"\\rightarrow, \\text{...}, \\times or \\le: they are shown to the user exactly as written, dollars " +
			"and backslashes included. Write the character itself (-> or →, ×, ≤), plain words, or a fenced code " +
			"block. Ordinary Markdown — headings, lists, tables, **bold**, `code` — renders correctly and is welcome.",
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
