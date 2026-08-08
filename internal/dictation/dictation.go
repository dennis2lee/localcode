// Package dictation turns a live microphone stream into text for the
// prompt box: partial results while someone is still speaking, and a
// final one when they stop.
//
// The recognizer itself is behind an interface and behind a build tag.
// Speech recognition needs CGo, which the pure-Go builds the release
// pipeline cross-compiles from one machine deliberately leave out — the
// same arrangement internal/gui already uses for its native window, and
// the reason this feature is desktop-only. See sherpa.go (the real one,
// -tags gui) and stub.go (everything else).
package dictation

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// SampleRate is the only rate this package accepts. Browsers can capture
// at whatever the device prefers and resample in an AudioWorklet, so
// fixing it here keeps the resampling in one place — the client — instead
// of having every recognizer implementation deal with it.
const SampleRate = 16000

// ErrUnavailable is returned when this build has no recognizer compiled
// in. Callers turn it into an explanation rather than a failure: a build
// without speech support should say so, not look broken.
var ErrUnavailable = errors.New("this build has no speech recognizer (desktop builds only)")

// Recognizer is a streaming speech-to-text engine. Implementations are
// not required to be safe for concurrent use — Session serializes access.
type Recognizer interface {
	// Accept feeds mono float32 samples at SampleRate.
	Accept(samples []float32)
	// Partial returns the text for everything fed so far. It is expected
	// to change as more audio arrives, including earlier words: that is
	// what makes it provisional, and why the UI shows it in grey.
	Partial() string
	// Endpoint reports whether the speaker has paused long enough that
	// the current utterance is finished.
	Endpoint() bool
	// Reset clears the current utterance, after its text has been taken.
	Reset()
	// Close releases the engine.
	Close()
}

// Result is one exchange with a dictation session: the text that is
// settled, and the text still being revised.
type Result struct {
	// Final is any utterance that completed since the last call, ready to
	// be committed to the prompt box as ordinary text.
	Final string `json:"final,omitempty"`
	// Provisional is the current in-progress utterance. It is shown in
	// grey precisely because it is expected to change — a streaming
	// recognizer revises earlier words as later ones give it context.
	Provisional string `json:"provisional"`
}

// Session is one live dictation, owning a recognizer and the text it has
// produced so far.
type Session struct {
	mu   sync.Mutex
	rec  Recognizer
	done bool

	// lastActivity is used by the daemon to reap a session whose client
	// went away mid-utterance — a browser tab closed with the microphone
	// on would otherwise hold a recognizer (and, for a real engine, its
	// model) open forever.
	lastActivity time.Time
}

func NewSession(rec Recognizer) *Session {
	return &Session{rec: rec, lastActivity: time.Now()}
}

// Write feeds one chunk of 16-bit little-endian PCM and returns what the
// recognizer makes of the audio so far.
//
// PCM16 rather than float32 on the wire because it halves the bytes for
// no audible loss at this sample rate, and because it is what an
// AudioWorklet can produce without a conversion pass of its own.
func (s *Session) Write(pcm []byte) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return Result{}, errors.New("dictation session already stopped")
	}
	if len(pcm)%2 != 0 {
		return Result{}, fmt.Errorf("pcm length %d is not a whole number of 16-bit samples", len(pcm))
	}
	s.lastActivity = time.Now()

	s.rec.Accept(decodePCM16(pcm))

	var res Result
	// Endpoint first: if the speaker has stopped, whatever the recognizer
	// has becomes final and the next audio starts a fresh utterance. Doing
	// this before reading Partial is what keeps a finished sentence from
	// being reported as both final and provisional in the same reply.
	if s.rec.Endpoint() {
		res.Final = s.rec.Partial()
		s.logUtterance(res.Final)
		s.rec.Reset()
	}
	res.Provisional = s.rec.Partial()
	return res, nil
}

// Stop ends the session and returns whatever was still in progress, so a
// half-finished sentence isn't silently dropped when someone clicks the
// microphone off mid-word.
func (s *Session) Stop() Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return Result{}
	}
	s.done = true
	res := Result{Final: s.rec.Partial()}
	s.logUtterance(res.Final)
	s.rec.Close()
	return res
}

// Idle reports how long since this session last received audio.
func (s *Session) Idle() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.lastActivity)
}

// decodePCM16 converts 16-bit little-endian PCM to the float32 samples
// every recognizer here wants, scaled to [-1, 1).
func decodePCM16(pcm []byte) []float32 {
	out := make([]float32, len(pcm)/2)
	for i := range out {
		v := int16(binary.LittleEndian.Uint16(pcm[i*2:]))
		// Divided by 32768 rather than MaxInt16 so the scale is an exact
		// power of two: the full-scale negative sample (-32768) maps to
		// exactly -1.0 instead of just past it.
		out[i] = float32(v) / 32768
	}
	return out
}

// tokenReporter is a recognizer that can show the pieces behind its text.
// Optional: a recognizer without it simply reports no tokens.
type tokenReporter interface{ Tokens() []string }

// logUtterance prints a finished utterance and the tokens behind it when
// LC_DICTATION_DEBUG is set.
//
// The same information `localcode dictation test` gives for a recorded
// file, but from the microphone in ordinary use — because asking someone
// to produce a 16 kHz mono WAV before their problem can be looked at is a
// good way never to see the problem. It stays off by default: this is one
// line per sentence on stderr, and the text of it is what the person just
// said out loud.
func (s *Session) logUtterance(text string) {
	if text == "" || os.Getenv("LC_DICTATION_DEBUG") == "" {
		return
	}
	var tokens []string
	if tr, ok := s.rec.(tokenReporter); ok {
		tokens = tr.Tokens()
	}
	marks, empty := summarize(tokens)
	log.Printf("dictation: text=%q tokens=%q (%d start a word, %d decoded to nothing)", text, tokens, marks, empty)
}
