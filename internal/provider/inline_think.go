package provider

import "strings"

// Reasoning a server sends inside the answer.
//
// A reasoning model's server can report its reasoning two ways. The
// separated way puts it in its own field (reasoning_content or
// reasoning), which the OpenAI-compatible adapter has always read. The
// inline way puts it at the start of the answer itself, wrapped in
// <think>…</think>: LM Studio does this when its "separate
// reasoning_content" developer setting is off, and llama.cpp, older
// Ollama and servers without a reasoning parser do it for the models
// whose templates use think tags. Read as the answer, the reasoning was
// drawn as the reply's first paragraphs, tag and all, went back to the
// model on every later request as though it had said it, and went into
// compaction summaries.
//
// inlineThink takes the answer's text as it streams and splits a
// leading <think>…</think> off it as reasoning. Only a block at the very
// start counts, leading whitespace aside: a model that writes the tag
// later in an answer, say while explaining think tags, is quoting it.
// Tags split across deltas are held until they can be told apart.
type inlineThink struct {
	state int
	// held is text not yet handed on: the start of the answer while it
	// could still be an opening tag, or the end of the reasoning while it
	// could still be the start of a closing one.
	held string
	// trimNext trims leading whitespace off what follows a boundary: the
	// start of the reasoning after <think>, and the start of the answer
	// after </think>, which a model separates with blank lines.
	trimNext bool
}

const (
	inlineDeciding = iota // nothing but whitespace or part of "<think>" yet
	inlineInside          // inside <think>…
	inlineAnswer          // the answer: everything passes through
)

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

// feed takes the next piece of the answer and returns what of it is
// reasoning and what is answer.
func (t *inlineThink) feed(s string) (reasoning, text string) {
	switch t.state {
	case inlineAnswer:
		return "", t.trimmed(s)
	case inlineDeciding:
		t.held += s
		rest := strings.TrimLeft(t.held, " \t\r\n")
		switch {
		case rest == "" || strings.HasPrefix(thinkOpen, rest):
			// Whitespace, or the tag so far: wait for more.
			return "", ""
		case strings.HasPrefix(rest, thinkOpen):
			t.state = inlineInside
			t.held = ""
			t.trimNext = true
			return t.feed(rest[len(thinkOpen):])
		default:
			// An answer with no reasoning in it, passed on as it came.
			t.state = inlineAnswer
			out := t.held
			t.held = ""
			return "", out
		}
	default: // inlineInside
		buf := t.held + s
		t.held = ""
		if i := strings.Index(buf, thinkClose); i >= 0 {
			reasoning = t.trimmed(buf[:i])
			t.state = inlineAnswer
			t.trimNext = true
			return reasoning, t.trimmed(buf[i+len(thinkClose):])
		}
		// Hold back a tail that could be the start of the closing tag.
		// It is made of ASCII, so the cut is on a character boundary.
		keep := 0
		for n := len(thinkClose) - 1; n > 0; n-- {
			if strings.HasSuffix(buf, thinkClose[:n]) {
				keep = n
				break
			}
		}
		t.held = buf[len(buf)-keep:]
		return t.trimmed(buf[:len(buf)-keep]), ""
	}
}

// trimmed applies a pending trim to s, and keeps it pending while s is
// all whitespace.
func (t *inlineThink) trimmed(s string) string {
	if !t.trimNext {
		return s
	}
	s = strings.TrimLeft(s, " \t\r\n")
	if s != "" {
		t.trimNext = false
	}
	return s
}

// flush ends the answer: whatever is held goes where it belongs. A
// reasoning block never closed is reasoning to the end, and a start that
// never became a tag is answer. Nothing after it is split.
func (t *inlineThink) flush() (reasoning, text string) {
	held := t.held
	t.held = ""
	state := t.state
	t.state = inlineAnswer
	switch state {
	case inlineInside:
		return t.trimmed(held), ""
	case inlineDeciding:
		if strings.TrimSpace(held) == "" {
			return "", ""
		}
		return "", held
	}
	return "", ""
}

// inside reports whether the answer is inside an inline reasoning block
// now, so a tool call can end it.
func (t *inlineThink) inside() bool { return t.state == inlineInside }
