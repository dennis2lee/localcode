package dictation

import (
	"encoding/binary"
	"strings"
	"testing"
)

// fakeRecognizer stands in for a real engine: it appends a word per
// chunk and declares an endpoint when told to, which is all Session's
// behaviour depends on.
type fakeRecognizer struct {
	words       []string
	endpointNow bool
	closed      bool
	accepted    int
}

func (f *fakeRecognizer) Accept(samples []float32) {
	f.accepted += len(samples)
	f.words = append(f.words, "word")
}
func (f *fakeRecognizer) Partial() string { return strings.Join(f.words, " ") }
func (f *fakeRecognizer) Endpoint() bool  { return f.endpointNow }
func (f *fakeRecognizer) Reset()          { f.words = nil; f.endpointNow = false }
func (f *fakeRecognizer) Close()          { f.closed = true }

// pcm builds n samples of 16-bit little-endian audio.
func pcm(values ...int16) []byte {
	b := make([]byte, len(values)*2)
	for i, v := range values {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
	}
	return b
}

func TestWriteReportsProvisionalTextUntilAnEndpoint(t *testing.T) {
	f := &fakeRecognizer{}
	s := NewSession(f)

	res, err := s.Write(pcm(1, 2, 3, 4))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if res.Provisional != "word" {
		t.Errorf("provisional = %q, want %q", res.Provisional, "word")
	}
	if res.Final != "" {
		t.Errorf("final = %q, want empty — the speaker hasn't stopped", res.Final)
	}

	res, _ = s.Write(pcm(5, 6))
	if res.Provisional != "word word" {
		t.Errorf("provisional = %q, want it to have grown", res.Provisional)
	}
}

// The whole point of the two fields: when the speaker pauses, the text
// settles and the next audio starts a new utterance. A sentence must not
// come back as both final and provisional in the same reply, or a client
// would show it twice.
func TestAnEndpointSettlesTheTextAndStartsFresh(t *testing.T) {
	f := &fakeRecognizer{}
	s := NewSession(f)
	s.Write(pcm(1, 2))
	f.endpointNow = true

	res, err := s.Write(pcm(3, 4))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if res.Final != "word word" {
		t.Errorf("final = %q, want the finished utterance", res.Final)
	}
	if res.Provisional != "" {
		t.Errorf("provisional = %q, want empty — the utterance just ended and was reported as final", res.Provisional)
	}

	res, _ = s.Write(pcm(5, 6))
	if res.Provisional != "word" {
		t.Errorf("provisional = %q, want a fresh utterance after the endpoint", res.Provisional)
	}
}

// Clicking the microphone off mid-word must not throw the word away.
func TestStopReturnsTheUnfinishedSentence(t *testing.T) {
	f := &fakeRecognizer{}
	s := NewSession(f)
	s.Write(pcm(1, 2))

	res := s.Stop()
	if res.Final != "word" {
		t.Errorf("final = %q, want the in-progress text", res.Final)
	}
	if !f.closed {
		t.Error("the recognizer was not closed")
	}
	if _, err := s.Write(pcm(3)); err == nil {
		t.Error("writing to a stopped session should fail")
	}
}

// Odd-length input means the client's framing is broken; decoding it
// anyway would silently drop or invent half a sample.
func TestOddLengthAudioIsRejected(t *testing.T) {
	s := NewSession(&fakeRecognizer{})
	if _, err := s.Write([]byte{1, 2, 3}); err == nil {
		t.Error("expected an error for a partial 16-bit sample")
	}
}

func TestPCMDecodesToTheFullFloatRange(t *testing.T) {
	got := decodePCM16(pcm(0, 32767, -32768, -16384))
	want := []float32{0, 32767.0 / 32768.0, -1, -0.5}
	if len(got) != len(want) {
		t.Fatalf("decoded %d samples, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sample %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// A model directory that isn't one should say what is missing. "model
// not found" is a dead end for the usual mistake, which is unpacking the
// archive one level too deep.
func TestResolveModelNamesWhatIsMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := resolveModel(dir)
	if err == nil {
		t.Fatal("expected an error for an empty directory")
	}
	for _, want := range []string{"encoder", "decoder", "joiner", "tokens.txt"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}

	if _, err := resolveModel(""); err == nil {
		t.Error("expected an error when no model directory is configured")
	}
}
