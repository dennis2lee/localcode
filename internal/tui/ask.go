package tui

import (
	"fmt"
	"strings"
)

// A question the model asked mid-turn, and the numbered answers to it.
//
// Numbered rather than lettered, and answered by typing the digit, for
// the same reason the permission modal takes single letters: the person
// is watching a turn run and the cheapest possible answer is one
// keystroke. Anything they type instead is sent as the answer in their
// own words, which is the escape hatch that keeps a badly-framed
// question from being a dead end.
type pendingAsk struct {
	id       string
	question string
	options  []string
}

// prompt renders the question and its answers.
func (a pendingAsk) prompt(typing bool) string {
	var b strings.Builder
	b.WriteString("The model is asking: " + a.question)
	for i, opt := range a.options {
		fmt.Fprintf(&b, "\n  %d. %s", i+1, opt)
	}
	if typing {
		// The digits are ordinary characters while there is a message in
		// the box, and nothing on screen would otherwise say so.
		b.WriteString("\n(press enter to send what you have typed as the answer, " +
			"or clear the box to answer by number)")
	} else {
		b.WriteString("\n(press a number, or type your own answer and press enter)")
	}
	return b.String()
}

// answerFor maps a keystroke to the option it names, or "" when the key
// is not one of them.
func (a pendingAsk) answerFor(key string) string {
	for i := range a.options {
		if key == fmt.Sprintf("%d", i+1) {
			return a.options[i]
		}
	}
	return ""
}
