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
	"sync/atomic"
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
	// Error is a transcription failure worth telling the user about,
	// reported once and then cleared.
	//
	// Failures used to be dropped everywhere: a failed partial on the
	// grounds that the next one is a second away, and a failed final on
	// the grounds that the last good partial is better than nothing.
	// Both are right in isolation and together they add up to dictation
	// that cannot fail out loud — a microphone that is on, audio going
	// out four times a second, every request refused, and nothing on
	// screen. That is what a remote server speaking a different protocol
	// looked like: not an error, just silence.
	Error string `json:"error,omitempty"`
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
	//
	// Atomic rather than under mu, and that is not a micro-optimisation:
	// committing an utterance calls into the engine and waits up to a
	// minute for the answer, all while holding mu. The reaper reads Idle
	// for every session while holding the *manager* lock, so if Idle
	// needed mu, one wedged engine would stop the reaper, and a stopped
	// reaper holding the manager lock blocks Start, Get and Stop — that
	// is, every other client's audio, every new dictation, and every
	// attempt to switch the microphone off. One slow transcription froze
	// dictation for everyone.
	lastActivity atomic.Int64 // unix nanos
}

func NewSession(rec Recognizer) *Session {
	s := &Session{rec: rec}
	s.touch()
	return s
}

func (s *Session) touch() { s.lastActivity.Store(time.Now().UnixNano()) }

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
	s.touch()
	// And again on the way out, however long the engine took. Committing an
	// utterance calls Final, which re-reads the whole thing and is allowed
	// a minute to do it; on a slow machine that is a minute in which this
	// session receives no audio, because the client sends one chunk at a
	// time and is waiting on this very request. The reaper only looks at
	// when audio last *arrived*, so a long transcription made the session
	// look abandoned and it was closed underneath the microphone — the
	// next chunk got "no dictation session" and dictation stopped
	// mid-sentence.
	defer s.touch()

	s.rec.Accept(decodePCM16(pcm))

	var res Result
	// Endpoint first: if the speaker has stopped, whatever the recognizer
	// has becomes final and the next audio starts a fresh utterance. Doing
	// this before reading Partial is what keeps a finished sentence from
	// being reported as both final and provisional in the same reply.
	if s.rec.Endpoint() {
		res.Final = s.settled()
		s.logUtterance(res.Final)
		s.rec.Reset()
	}
	res.Provisional = s.rec.Partial()
	res.Error = s.takeError()
	return res, nil
}

// errorReporter is implemented by a recognizer that can say why it
// produced nothing. Optional: one that cannot simply never reports.
type errorReporter interface {
	// TakeError returns the last failure and clears it, so a persistent
	// fault is reported once per occurrence rather than on every chunk.
	TakeError() string
}

func (s *Session) takeError() string {
	if r, ok := s.rec.(errorReporter); ok {
		return r.TakeError()
	}
	return ""
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
	res := Result{Final: s.settled()}
	s.logUtterance(res.Final)
	s.rec.Close()
	return res
}

// Idle reports how long since this session last received audio.
func (s *Session) Idle() time.Duration {
	return time.Since(time.Unix(0, s.lastActivity.Load()))
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

// finalizer is a recognizer whose committed text is worth more work than
// its running text. Optional: a recognizer without it commits whatever
// Partial says.
//
// A streaming transducer has nothing to add here, since its partial
// already reflects every sample it has been given. A window-at-a-time
// model does: its partials are periodic snapshots, so the audio since
// the last one is missing from the text, and that audio is the end of
// the sentence. Final re-reads the whole utterance.
type finalizer interface{ Final() string }

// settled returns the text to commit for the utterance just ended.
func (s *Session) settled() string {
	if f, ok := s.rec.(finalizer); ok {
		return f.Final()
	}
	return s.rec.Partial()
}

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
