package dictation

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"
)

// partialInterval is how often the in-progress utterance is re-read.
//
// Whisper is not a streaming model: it takes a window of audio and
// returns the whole of it, so the only way to show text while someone is
// still talking is to keep re-transcribing the utterance so far. That is
// wasted work by construction, and this interval is the price of it.
//
// Measured at ~290ms for 6.6s of audio on Apple Silicon, so once a
// second leaves the engine idle most of the time and still updates the
// grey text faster than most people finish a clause. A slower machine
// simply falls behind gracefully: a request in flight suppresses the
// next one rather than queueing it.
const partialInterval = 900 * time.Millisecond

// whisperRecognizer adapts a window-at-a-time model to the streaming
// Recognizer interface.
//
// Audio accumulates; a transcription of everything accumulated runs in
// the background at most once per partialInterval; Partial returns the
// most recent one. Endpoint comes from the VAD rather than the model,
// because the model has no opinion about when a speaker stopped.
type whisperRecognizer struct {
	proc     *whisperProcess
	language string

	mu       sync.Mutex
	audio    []float32
	text     string
	detector vad
	running  bool
	lastRun  time.Time
	closed   bool

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func openWhisper(cfg Config) (Recognizer, error) {
	proc, err := acquireWhisper(cfg)
	if err != nil {
		return nil, err
	}
	return &whisperRecognizer{proc: proc, language: cfg.Language}, nil
}

func (w *whisperRecognizer) Accept(samples []float32) {
	if len(samples) == 0 {
		return
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.audio = append(w.audio, samples...)
	w.detector.feed(samples)

	// One request at a time. Queueing them would let a slow machine build
	// a backlog of transcriptions of audio that has already been
	// superseded, and the newest window contains everything the older
	// ones did.
	if w.running || time.Since(w.lastRun) < partialInterval || !w.detector.spoke() {
		w.mu.Unlock()
		return
	}
	w.running = true
	w.lastRun = time.Now()
	window := append([]float32(nil), w.audio...)
	w.mu.Unlock()

	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		text, err := w.proc.transcribe(ctx, window, w.language)

		w.mu.Lock()
		defer w.mu.Unlock()
		w.running = false
		// A failed partial is dropped rather than surfaced: the next one
		// is a second away and covers the same audio, and replacing good
		// grey text with an error message helps nobody mid-sentence. A
		// failure that matters will fail the final transcription too,
		// where it is reported.
		if err == nil && !w.closed {
			w.text = text
		}
	}()
}

func (w *whisperRecognizer) Partial() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.text
}

// Endpoint reports that the speaker has paused long enough to call the
// utterance finished.
func (w *whisperRecognizer) Endpoint() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.detector.feed(nil)
}

// Final transcribes the complete utterance and waits for the answer.
//
// Separate from Partial because the partials are a preview and this is
// the text that gets committed: it covers every sample including the
// ones that arrived after the last background run started, and it is
// worth blocking a moment to get right.
func (w *whisperRecognizer) Final() string {
	w.mu.Lock()
	if w.closed || !w.detector.spoke() {
		w.mu.Unlock()
		return ""
	}
	window := append([]float32(nil), w.audio...)
	w.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	text, err := w.proc.transcribe(ctx, window, w.language)
	if err != nil {
		// Fall back to the last good partial rather than losing the
		// sentence: an imperfect transcript beats silently dropping
		// what someone just said.
		return w.Partial()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.text = text
	return text
}

func (w *whisperRecognizer) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.audio = w.audio[:0]
	w.text = ""
	w.detector.reset()
	w.lastRun = time.Time{}
}

func (w *whisperRecognizer) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.mu.Unlock()

	// Background partials are waited for rather than abandoned: they hold
	// a reference to the shared process, and releasing it under them is
	// how a "use after free" gets written in Go.
	w.wg.Wait()
	w.proc.release()
}

// hallucination matches the bracketed annotations Whisper emits for
// non-speech — "[BLANK_AUDIO]", "(음악)", "[Music]".
//
// It emits them for silence in particular, and dictation is mostly
// silence: pausing to think would otherwise type "[BLANK_AUDIO]" into
// the prompt box.
var hallucination = regexp.MustCompile(`(?s)[\[(（【][^\])）】]{0,40}[\])）】]`)

// cleanTranscript turns one engine reply into text fit for a prompt box.
func cleanTranscript(s string) string {
	s = hallucination.ReplaceAllString(s, "")
	// Whisper puts a leading space on every segment and a trailing
	// newline on the reply; joined segments leave runs of space behind.
	return strings.Join(strings.Fields(s), " ")
}
