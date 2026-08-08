package dictation

import "strings"

// wordMark is the sentencepiece marker for the start of a word. Some
// models emit it literally; others have already had it turned into a
// plain space by the time the tokens reach us.
const wordMark = "▁"

// joinTokens rebuilds a transcript from the tokens behind it, and reports
// whether it had anything better to offer than the text it was given.
//
// This exists because sherpa-onnx can hand back tokens that carry word
// boundaries and a finished string that does not. Measured on Windows
// with the Korean model:
//
//	text   = "는구체적인돈을남겼어."
//	tokens = ["는" " 구" "체" "적인" " 돈을" " 남" "겼" "어" "."]
//
// The transcript is right and the spacing is right; only the joining
// step lost it. Since the tokens are the model's own output, rebuilding
// from them here recovers the spacing without touching the model, the
// configuration, or anything in C.
//
// It is deliberately conservative. A rebuild is only preferred when the
// tokens mark boundaries and the given text has none — the exact shape
// of the fault. Anything else keeps the recognizer's own text, so models
// that were already correct (English, and anything whose tokens carry no
// marks) are unaffected.
func joinTokens(text string, tokens []string) (string, bool) {
	if len(tokens) == 0 || strings.Contains(text, " ") {
		return text, false
	}
	var b strings.Builder
	marked := false
	for _, t := range tokens {
		if strings.HasPrefix(t, wordMark) {
			marked = true
			b.WriteByte(' ')
			b.WriteString(strings.TrimPrefix(t, wordMark))
			continue
		}
		if strings.HasPrefix(t, " ") {
			marked = true
		}
		b.WriteString(t)
	}
	if !marked {
		return text, false
	}
	// TrimSpace, not TrimLeft: the first token of an utterance usually
	// carries a boundary mark of its own, and a transcript that starts
	// with a space would be pasted into the prompt box as one.
	return strings.TrimSpace(b.String()), true
}
