package agent

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// What a permission question shows is what the person approves.
//
// A tool that builds its own question puts the model's own words in it: the
// prompt a Schedule call books, the task a Debate is given, the address a
// browser opens. Those words were written after the model read whatever it
// read, and two things let them say less on screen than they do:
//
//   - Schedule and Debate showed the text up to its first line break, so a
//     prompt of two paragraphs was approved as its first.
//   - Nothing cleaned the text. A carriage return, the ANSI conceal sequence
//     and a right-to-left override reached the TUI's modal as they were, and
//     a terminal draws them: text the person was never shown, approved.
//
// So the question carries the whole text (confirmationText), and the
// broker turns every character a screen would act on rather than show into
// an escape a reader can see (events.VisibleText). The second is done in
// one place, where the request is written, so a tool added later is
// covered without knowing about it.

// maxConfirmationRunes bounds the model's text in a permission question.
//
// High enough that no prompt anybody writes reaches it, and there only so
// that a runaway one does not put megabytes into a log event. Past it the
// question says exactly how much it left out, so the bound is never a
// place for text to hide without saying so.
const maxConfirmationRunes = 8000

// confirmationText is the model's text as a permission question shows it:
// all of it, trimmed, up to maxConfirmationRunes, and an exact count of
// what was left out beyond that.
//
// Not promptSummary, which is one line for a list row and is also what
// Subject is built from. Permission rules match on Subject, so it stays
// as it was; only what the person reads changes.
func confirmationText(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= maxConfirmationRunes {
		return s
	}
	runes := []rune(s)
	left := len(runes) - maxConfirmationRunes
	return string(runes[:maxConfirmationRunes]) + fmt.Sprintf(" … (%d more characters not shown)", left)
}
