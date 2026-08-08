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

// maxUtterance caps how much audio one utterance may accumulate.
//
// The endpoint detector needs silence, and there are rooms that never
// provide any: a fan, a conversation at the next desk, a microphone left
// open. Without a cap nothing would ever commit, the buffer would grow
// for as long as the session lived, and every partial would re-send an
// ever-larger recording — the work per second rising with the time spent
// so far.
//
// Thirty seconds because that is the window Whisper itself reads as a
// unit, so nothing is gained by holding more before committing. The old
// engine had the same idea in sherpa's Rule3MinUtteranceLength.
const maxUtterance = 30 * SampleRate

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
	// forced records that the utterance hit maxUtterance and has to end
	// even though the speaker has not paused.
	forced bool
	// utterance counts utterances, so a transcription that comes back
	// after its own has ended can tell. Without it, a partial still in
	// flight when the speaker pauses lands in the *next* utterance: the
	// sentence just committed reappears as grey text under the sentence
	// now being spoken.
	utterance uint64

	wg sync.WaitGroup
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
	if len(w.audio) >= maxUtterance {
		w.forced = true
	}

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
	gen := w.utterance
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
		if err != nil || w.closed {
			// A failed partial is dropped rather than surfaced: the next
			// one is a second away and covers the same audio, and
			// replacing good grey text with an error message helps
			// nobody mid-sentence. A failure that matters will fail the
			// final transcription too, where it is reported.
			return
		}
		if gen != w.utterance {
			return // this is the previous utterance's text
		}
		w.text = text
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
	return w.forced || w.detector.feed(nil)
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
	// A fresh slice, not audio[:0]: the previous utterance's samples are
	// still referenced by any transcription in flight, and reusing the
	// array would rewrite the audio underneath it.
	w.audio = nil
	w.text = ""
	w.forced = false
	w.detector.reset()
	w.lastRun = time.Time{}
	w.utterance++
}

func (w *whisperRecognizer) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.utterance++
	w.mu.Unlock()

	// Background partials are waited for rather than abandoned: they hold
	// a reference to the shared process, and releasing it under them is
	// how a "use after free" gets written in Go.
	w.wg.Wait()
	w.proc.release()
}

// Whisper narrates non-speech in brackets — "[BLANK_AUDIO]", "[Music]",
// "(음악)" — and dictation is mostly silence, so a pause to think would
// otherwise type "[BLANK_AUDIO]" into the prompt box.
//
// Only these are removed, not every bracketed group. Stripping anything
// in brackets is the obvious rule and it deletes what people actually
// say: "이 함수(비동기)를 async로 바꿔줘" came out as "이 함수를 async로
// 바꿔줘", losing a word with nothing on screen to show it had gone.
// Leaving a stray annotation in is a visible mistake the speaker can
// delete; silently dropping their words is not.
var (
	bracketed = regexp.MustCompile(`(?s)[\[(（【]([^\])）】]*)[\])）】]`)

	// The annotations Whisper actually emits. Matched case-insensitively
	// against the bracket's contents with spaces and underscores
	// collapsed, so "[BLANK_AUDIO]" and "[ blank audio ]" both go.
	annotations = map[string]bool{
		"blankaudio": true, "blank": true, "silence": true, "noise": true,
		"music": true, "applause": true, "laughter": true, "laugh": true,
		"sound": true, "inaudible": true, "coughing": true, "cough": true,
		"음악": true, "박수": true, "웃음": true, "잡음": true, "침묵": true,
		"소리": true, "무음": true,
	}
)

// cleanTranscript turns one engine reply into text fit for a prompt box.
func cleanTranscript(s string) string {
	s = bracketed.ReplaceAllStringFunc(s, func(m string) string {
		inner := bracketed.FindStringSubmatch(m)[1]
		key := strings.ToLower(strings.NewReplacer(" ", "", "_", "", "-", "").Replace(inner))
		if annotations[key] {
			return ""
		}
		return m
	})
	// Whisper puts a leading space on every segment and a trailing
	// newline on the reply; joined segments leave runs of space behind.
	s = strings.Join(strings.Fields(s), " ")

	// A reply that is nothing but one bracketed group was an annotation
	// this does not know the name of, since a speaker who said only
	// "(something)" and nothing else is not a case worth preserving.
	if inner := bracketed.FindStringSubmatch(s); inner != nil && inner[0] == s {
		return ""
	}
	return s
}
