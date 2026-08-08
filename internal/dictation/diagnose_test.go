package dictation

import (
	"bytes"
	"encoding/binary"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestWAV(t *testing.T, dir string, channels, rate, bits int, samples int) string {
	t.Helper()
	data := make([]byte, samples*2)
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+len(data)))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint16(channels))
	binary.Write(&b, binary.LittleEndian, uint32(rate))
	binary.Write(&b, binary.LittleEndian, uint32(rate*channels*bits/8))
	binary.Write(&b, binary.LittleEndian, uint16(channels*bits/8))
	binary.Write(&b, binary.LittleEndian, uint16(bits))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)

	p := filepath.Join(dir, "a.wav")
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A diagnosis run on audio the recognizer was never going to understand
// is worse than none: it blames the model for the file. So each way a
// recording can be unusable is named specifically.
func TestReadWAVRejectsAudioTheModelCannotUse(t *testing.T) {
	dir := t.TempDir()

	if _, _, err := ReadWAV(writeTestWAV(t, dir, 2, SampleRate, 16, 100)); err == nil || !strings.Contains(err.Error(), "mono") {
		t.Errorf("stereo: err = %v, want it to name the channel count", err)
	}
	if _, _, err := ReadWAV(writeTestWAV(t, dir, 1, 44100, 16, 100)); err == nil || !strings.Contains(err.Error(), "Hz") {
		t.Errorf("44.1 kHz: err = %v, want it to name the sample rate", err)
	}
	if _, _, err := ReadWAV(writeTestWAV(t, dir, 1, SampleRate, 8, 100)); err == nil || !strings.Contains(err.Error(), "8-bit") {
		t.Errorf("8-bit: err = %v, want it to name the bit depth", err)
	}

	samples, rate, err := ReadWAV(writeTestWAV(t, dir, 1, SampleRate, 16, 8000))
	if err != nil {
		t.Fatalf("16 kHz mono 16-bit should be accepted: %v", err)
	}
	if rate != SampleRate || len(samples) != 8000 {
		t.Errorf("read %d samples at %d Hz, want 8000 at %d", len(samples), rate, SampleRate)
	}
}

// The whole point of the diagnosis is that two different faults look the
// same in the finished text, so the reading has to name which one it is
// looking at.
func TestDiagnosisTellsTheTwoFaultsApart(t *testing.T) {
	// Correct tokens, joined without the spaces their marks call for.
	joining := Diagnosis{Text: "그는괜찮은척하려고", Tokens: []string{"▁그", "는", "▁괜찮", "은", "▁척"}}
	joining.WordMarks, joining.Empty = summarize(joining.Tokens)
	if got := joining.String(); !strings.Contains(got, "not being turned into spaces") {
		t.Errorf("a joining fault was not identified as one:\n%s", got)
	}

	// Pieces lost between the model and the text — what Whisper does to
	// Korean.
	dropping := Diagnosis{Text: "그는 괜찮은  하고", Tokens: []string{"▁그", "는", "▁괜찮", "은", "", ""}}
	dropping.WordMarks, dropping.Empty = summarize(dropping.Tokens)
	if got := dropping.String(); !strings.Contains(got, "decoded to nothing") {
		t.Errorf("dropped pieces were not identified:\n%s", got)
	}

	// Decoding is fine; if the text is wrong the model is mishearing.
	fine := Diagnosis{Text: "그는 괜찮은 척하려고", Tokens: []string{"▁그", "는", "▁괜찮", "은", "▁척하려고"}}
	fine.WordMarks, fine.Empty = summarize(fine.Tokens)
	if got := fine.String(); !strings.Contains(got, "mishearing") {
		t.Errorf("a healthy decode should point at the model instead:\n%s", got)
	}

	// Nothing at all is neither, and must not be reported as either.
	empty := Diagnosis{}
	if got := empty.String(); !strings.Contains(got, "nothing at all") {
		t.Errorf("an empty result was not identified:\n%s", got)
	}
}

// recordingRecognizer is a fake with tokens, so the debug path can be
// exercised without a real engine.
type recordingRecognizer struct {
	text   string
	tokens []string
}

func (r *recordingRecognizer) Accept([]float32) {}
func (r *recordingRecognizer) Partial() string  { return r.text }
func (r *recordingRecognizer) Tokens() []string { return r.tokens }
func (r *recordingRecognizer) Endpoint() bool   { return false }
func (r *recordingRecognizer) Reset()           {}
func (r *recordingRecognizer) Close()           {}

// Asking someone to produce a 16 kHz mono WAV before their problem can be
// looked at is a good way never to see the problem. With the switch on,
// ordinary microphone use reports the same thing the file command does.
func TestLiveDictationCanReportItsTokens(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	rec := &recordingRecognizer{text: "그는괜찮은", tokens: []string{"▁그", "는", "▁괜찮", "은"}}

	// Off by default: this prints what the person just said out loud.
	s := NewSession(rec)
	s.Stop()
	if buf.Len() != 0 {
		t.Errorf("logged without the switch set: %q", buf.String())
	}

	t.Setenv("LC_DICTATION_DEBUG", "1")
	buf.Reset()
	NewSession(rec).Stop()

	got := buf.String()
	if !strings.Contains(got, "그는괜찮은") {
		t.Errorf("did not report the text: %q", got)
	}
	if !strings.Contains(got, "▁괜찮") {
		t.Errorf("did not report the tokens, which is the whole point: %q", got)
	}
	if !strings.Contains(got, "2 start a word") {
		t.Errorf("did not count the word marks: %q", got)
	}
}
