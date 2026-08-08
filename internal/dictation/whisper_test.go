package dictation

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Whisper narrates non-speech in brackets, and dictation is mostly
// non-speech: every pause to think would otherwise type "[BLANK_AUDIO]"
// into the prompt box.
func TestCleanTranscript(t *testing.T) {
	tests := []struct{ in, want string }{
		{" 그는 괜찮은 척 하려고 애쓰는 것 같았다\n", "그는 괜찮은 척 하려고 애쓰는 것 같았다"},
		{"[BLANK_AUDIO]", ""},
		{" [Music] hello there\n", "hello there"},
		{"（음악）안녕하세요", "안녕하세요"},
		{"  spaced   out  \n\n", "spaced out"},
		{"", ""},
		// A bracket run longer than any real annotation is left alone,
		// so dictating an actual parenthetical does not lose it.
		{"(" + strings.Repeat("x", 60) + ")", "(" + strings.Repeat("x", 60) + ")"},
	}
	for _, tc := range tests {
		if got := cleanTranscript(tc.in); got != tc.want {
			t.Errorf("cleanTranscript(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The WAV this builds is the only thing the engine ever sees, so a
// wrong header would look exactly like the model mishearing.
func TestWriteWAVRoundTrips(t *testing.T) {
	in := make([]float32, 1600)
	for i := range in {
		in[i] = float32(math.Sin(float64(i) * 0.05))
	}
	var buf bytes.Buffer
	if err := writeWAV(&buf, in); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "a.wav")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	// Read back with the strict reader dictation test uses: it rejects
	// anything not mono 16 kHz 16-bit by name.
	out, rate, err := ReadWAV(path)
	if err != nil {
		t.Fatalf("reading back what we wrote: %v", err)
	}
	if rate != SampleRate {
		t.Errorf("rate = %d, want %d", rate, SampleRate)
	}
	if len(out) != len(in) {
		t.Fatalf("got %d samples, wrote %d", len(out), len(in))
	}
	for i := range in {
		if math.Abs(float64(in[i]-out[i])) > 1e-4 {
			t.Fatalf("sample %d: got %v, wrote %v", i, out[i], in[i])
		}
	}
}

// The VAD is what decides when grey text becomes committed text, since
// whisper.cpp has no notion of a speaker stopping.
func TestVADEndpointsAfterSpeechThenSilence(t *testing.T) {
	var v vad

	if v.feed(silence(2 * SampleRate)) {
		t.Error("endpoint on silence alone, with nothing ever said")
	}
	if v.spoke() {
		t.Error("spoke = true after silence only")
	}

	if v.feed(tone(SampleRate)) {
		t.Error("endpoint while still speaking")
	}
	if !v.spoke() {
		t.Error("spoke = false after a second of speech")
	}

	// Less quiet than it takes to end an utterance.
	if v.feed(silence(SampleRate / 4)) {
		t.Error("endpoint after a quarter second of quiet, which is a pause between words")
	}
	if !v.feed(silence(SampleRate)) {
		t.Error("no endpoint after a second of quiet following speech")
	}

	v.reset()
	if v.spoke() || v.feed(nil) {
		t.Error("reset left state behind")
	}
}

// A cough is not an utterance.
func TestVADIgnoresTooShortASound(t *testing.T) {
	var v vad
	v.feed(tone(SampleRate / 20)) // 50ms
	if v.feed(silence(2 * SampleRate)) {
		t.Error("endpoint fired for a 50ms noise")
	}
}

// Audio arrives in whatever chunk size the client sends, and the frame
// size divides none of them. A boundary that swallowed the remainder
// would read as silence and end utterances early.
func TestVADIsIndependentOfChunkSize(t *testing.T) {
	speech := tone(SampleRate)
	var whole, split vad
	whole.feed(speech)

	for off := 0; off < len(speech); off += 999 {
		end := off + 999
		if end > len(speech) {
			end = len(speech)
		}
		split.feed(speech[off:end])
	}

	// Within one frame of each other: the split run holds a partial
	// frame back rather than measuring it short.
	if diff := whole.speechSamples - split.speechSamples; diff > vadFrame || diff < -vadFrame {
		t.Errorf("speech measured as %d in one chunk and %d in many", whole.speechSamples, split.speechSamples)
	}
}

func tone(n int) []float32 {
	s := make([]float32, n)
	for i := range s {
		s[i] = float32(0.3 * math.Sin(float64(i)*0.1))
	}
	return s
}

func silence(n int) []float32 { return make([]float32, n) }
