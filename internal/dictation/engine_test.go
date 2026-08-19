package dictation

import (
	"strings"
	"testing"
)

// The spoken language is a setting only some engines have.
//
// This is the report it comes from: "I said 'I'm a boy' in English and it
// wrote 아이엠어보이." That is not a microphone fault or a mis-transcription
// — it is a Korean-only model doing exactly what it can do with English
// audio, which is spell it out in Hangul. Sherpa is one model per language
// and the model localcode installs is Korean, so the Spoken language
// control does nothing at all for it while appearing to be in force.
func TestSherpaSaysTheSpokenLanguageDoesNotApplyToIt(t *testing.T) {
	note := spokenLanguageNote(EngineSherpa)
	if note == "" {
		t.Fatal("the sherpa engine ignores the spoken language and said nothing about it")
	}
	for _, want := range []string{"Korean", "Hangul", "dictation install"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note does not mention %q, so it does not explain the symptom or the way out: %s", want, note)
		}
	}
}

// Whisper takes the language per request, so there is nothing to say and
// the panel stays quiet. A warning that is always there is not read.
func TestWhisperHasNoLanguageNote(t *testing.T) {
	if note := spokenLanguageNote(EngineWhisper); note != "" {
		t.Errorf("whisper honours the spoken language; the panel should say nothing: %s", note)
	}
}
