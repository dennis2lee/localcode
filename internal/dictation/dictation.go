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
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
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

	// committed holds sentences that have finished transcribing and have
	// not been handed to the client yet, in the order they were spoken.
	// Its own lock because the transcriptions that fill it run on their own
	// goroutines — see commit — while Write is holding mu.
	//
	// A slot per sentence, claimed when the utterance ends rather than when
	// its text comes back, because those are not the same order. Each
	// transcription is its own request and a shorter sentence overtakes a
	// longer one that started first — so against an engine slow enough for
	// two to be in flight, which is any engine on another machine, the
	// sentences arrived in the prompt box back to front. Claiming the slot
	// at the point the person stopped speaking is what makes the order the
	// order they spoke in.
	committedMu sync.Mutex
	committed   []sentence
	// pending counts transcriptions still running, so Stop can wait a
	// moment for the last sentence rather than dropping it.
	pending sync.WaitGroup

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
// ctx is the client's request. Committing an utterance calls into the
// engine and waits for the answer, so a browser that has given up — or a
// tab that was closed — must be able to take that work away with it,
// rather than leaving this session's lock held for as long as the engine
// feels like taking.
func (s *Session) Write(ctx context.Context, pcm []byte) (Result, error) {
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
		s.commit(ctx)
	}
	res.Provisional = s.rec.Partial()
	// Whatever finished committing since the last chunk, in the order the
	// sentences were spoken.
	res.Final = s.takeCommitted()
	res.Error = s.takeError()
	return res, nil
}

// commit sends the finished utterance for transcription and moves on.
//
// It does not wait for the answer, and that is the whole point. This used
// to be a blocking call inside the request that delivered the audio, so
// the time the engine took was time the browser sat on an open POST, with
// this session's lock held and its own audio queueing up behind it. On a
// local engine that is a few hundred milliseconds and nobody notices; on a
// speech server on another machine it is however long that machine takes,
// and every mechanism built on top of it — the client's deadline, the
// session lock, the queue — turned that delay into a failure. There is
// nothing to wait for anyway: the text is delivered with the next chunk of
// audio, which is a quarter of a second away.
//
// A recognizer that cannot hand over its audio (sherpa, which decodes as
// it goes) keeps the old behaviour: its Partial is already the answer.
func (s *Session) commit(ctx context.Context) {
	async, ok := s.rec.(asyncFinalizer)
	if !ok {
		// The blocking path, for a recognizer that cannot hand its audio
		// over. It still runs on the request's own context, so a client
		// that gives up takes the work with it.
		text := s.settled(ctx)
		s.logUtterance(text)
		s.rec.Reset()
		s.queueCommitted(text)
		return
	}

	// The last preview of this utterance, kept before the reset clears it.
	// If the transcription of the finished audio comes back with nothing —
	// it failed, or it ran out of time — this is still what the person
	// said, and an imperfect transcript beats silently dropping a sentence.
	fallback := s.rec.Partial()
	window := async.TakeUtterance()
	s.rec.Reset()
	if len(window) == 0 {
		return
	}
	at := s.claimSentence()
	s.pending.Add(1)
	go func() {
		defer s.pending.Done()
		text := async.Transcribe(context.Background(), window)
		if text == "" {
			text = fallback
		}
		s.logUtterance(text)
		s.settleSentence(at, text)
	}()
}

// sentence is one finished utterance on its way back from the engine.
type sentence struct {
	text string
	// done marks that the transcription has finished — with text, or with
	// nothing at all, which is still an answer.
	done bool
}

// claimSentence reserves this utterance's place in the order before its
// transcription starts.
func (s *Session) claimSentence() int {
	s.committedMu.Lock()
	defer s.committedMu.Unlock()
	s.committed = append(s.committed, sentence{})
	return len(s.committed) - 1
}

// settleSentence fills in a claimed slot.
func (s *Session) settleSentence(at int, text string) {
	s.committedMu.Lock()
	defer s.committedMu.Unlock()
	if at < len(s.committed) {
		s.committed[at] = sentence{text: text, done: true}
	}
}

// queueCommitted records a sentence whose text is already known.
func (s *Session) queueCommitted(text string) {
	s.committedMu.Lock()
	defer s.committedMu.Unlock()
	s.committed = append(s.committed, sentence{text: text, done: true})
}

// takeCommitted hands over every sentence that is settled *and* has
// nothing unsettled in front of it, keeping the order they were spoken in.
//
// The hold-back is the point. A sentence whose transcription is still
// running is a gap, not an absence, and emitting the one behind it would
// put the words in the wrong order permanently — nothing later can move
// text that is already in the prompt box. Waiting costs a chunk or two of
// delay and is invisible; getting it wrong is not.
func (s *Session) takeCommitted() string {
	s.committedMu.Lock()
	defer s.committedMu.Unlock()
	var ready []string
	n := 0
	for _, sent := range s.committed {
		if !sent.done {
			break
		}
		n++
		if sent.text != "" {
			ready = append(ready, sent.text)
		}
	}
	s.committed = s.committed[n:]
	return strings.Join(ready, " ")
}

// asyncFinalizer is a recognizer whose finished utterance can be taken
// away and transcribed on its own, without holding up whatever else the
// recognizer is doing. Optional: see commit.
type asyncFinalizer interface {
	// TakeUtterance removes the audio accumulated so far and returns it.
	TakeUtterance() []float32
	// Transcribe turns one window of audio into text. A failure comes back
	// as "" — it is reported through TakeError like any other.
	Transcribe(ctx context.Context, window []float32) string
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
	// Before the lock, not after it.
	//
	// Write holds s.mu across the engine call that commits an utterance,
	// and against a speech server that has stopped answering that call is
	// the whole of its timeout — so a Stop that simply asked for the lock
	// queued behind it, and the microphone would not switch off. Cancelling
	// first ends whatever is in flight; the lock is then free almost at
	// once, and the final transcription below runs on a fresh context of
	// its own rather than the one just cancelled.
	if c, ok := s.rec.(canceler); ok {
		c.Cancel()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return Result{}
	}
	s.done = true

	// Whatever was still being spoken when the microphone went off is text
	// the person said and meant, so it is transcribed too — but on the
	// clock, because this answer is what the click is waiting for. An
	// engine that takes longer than that has already had its cancel (see
	// above), and the sentence is lost rather than the button appearing
	// stuck.
	s.commit(context.Background())
	waited := make(chan struct{})
	go func() {
		s.pending.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-time.After(stopGrace):
	}

	// The error too. Every other reply carries one and this one did not, so
	// a dictation whose only failure happened at the end — which is where a
	// slow engine's failures land — finished with nothing said at all: no
	// text, and no reason for there being none.
	res := Result{Final: s.takeCommitted(), Error: s.takeError()}
	s.rec.Close()
	return res
}

// stopGrace is how long switching the microphone off waits for the
// sentence in progress to come back from the engine.
//
// Eight seconds, not three. Three was chosen against a local engine, which
// answers in a few hundred milliseconds; a speech server on another machine
// takes as long as that machine takes, and the sentence being waited for is
// the last thing the person said — usually the whole point of switching the
// microphone off. Nothing is blocked in the meantime: the client turns the
// microphone off the moment the capture stops, and this wait only decides
// how late the final words land in the box. The client's own limit is above
// this one, so this is what normally decides.
const stopGrace = 8 * time.Second

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
type finalizer interface {
	Final(ctx context.Context) string
}

// canceler is a recognizer whose in-flight work can be abandoned. Optional:
// one that cannot is simply waited for.
type canceler interface{ Cancel() }

// settled returns the text to commit for the utterance just ended.
func (s *Session) settled(ctx context.Context) string {
	if f, ok := s.rec.(finalizer); ok {
		return f.Final(ctx)
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
