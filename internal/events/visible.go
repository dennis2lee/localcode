package events

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Text an event carries for a person to read, made safe to draw.
//
// A permission question puts a model's own words in front of the person
// who approves it, and a terminal or a browser acts on some characters
// rather than drawing them: a carriage return overwrites the line, the ANSI
// conceal sequence hides what follows, a right-to-left override reorders
// it. Here because both sides need the one definition: the agent writes a
// question through it, and the TUI reads one through it, since a log
// written before the agent did is still replayed.

// VisibleText returns s with every control and format character written as
// an escape: "\r", "\x1b", "\u202e". Line breaks and tabs stay, because a
// prompt of several paragraphs has to read as one and both clients draw
// them. Everything else that is not drawn as itself goes: C0 and C1
// controls, DEL, the bidirectional and zero-width marks (Unicode Cf), and
// the line and paragraph separators, which some renderers treat as breaks
// the text did not ask for.
func VisibleText(s string) string {
	if !needsEscape(s) {
		return s
	}
	var b strings.Builder
	for i, w := 0, 0; i < len(s); i += w {
		r, size := utf8.DecodeRuneInString(s[i:])
		w = size
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x80 && unicode.IsControl(r):
			fmt.Fprintf(&b, `\x%02x`, r)
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029':
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func needsEscape(s string) bool {
	for i, w := 0, 0; i < len(s); i += w {
		r, size := utf8.DecodeRuneInString(s[i:])
		w = size
		if r == utf8.RuneError && size == 1 {
			return true
		}
		if r == '\n' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return true
		}
	}
	return false
}
