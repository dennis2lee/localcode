package dictation

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeEngine stands in for whisper-server: same HTTP shape, controllable
// timing. Enough to drive whisperRecognizer through sequences that a
// real engine would only produce by luck.
type fakeEngine struct {
	srv    *httptest.Server
	proc   *whisperProcess
	delay  atomic.Int64 // nanoseconds
	replyN atomic.Int64
	reply  func(n int64) string
}

func newFakeEngine(t *testing.T, reply func(n int64) string) *fakeEngine {
	t.Helper()
	f := &fakeEngine{reply: reply}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d := f.delay.Load(); d > 0 {
			time.Sleep(time.Duration(d))
		}
		n := f.replyN.Add(1)
		fmt.Fprintf(w, `{"text":%q}`, f.reply(n))
	}))
	t.Cleanup(f.srv.Close)

	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(f.srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	f.proc = &whisperProcess{port: port}
	return f
}

// A partial transcription that is still in flight when the utterance
// ends must not land in the next one.
//
// The sequence is ordinary: speak, pause, keep speaking. Accept starts a
// background read of utterance one; the pause ends it and Reset clears
// the state; the background read then finishes and stores its text. What
// the speaker sees is the sentence they just committed reappearing as
// grey text under the sentence they are now saying.
func TestPartialInFlightDoesNotSurviveReset(t *testing.T) {
	f := newFakeEngine(t, func(n int64) string { return fmt.Sprintf("utterance-%d", n) })
	f.delay.Store(int64(300 * time.Millisecond))

	w := &whisperRecognizer{proc: f.proc}
	defer w.Close()

	// Start a partial and let it get as far as the network.
	w.Accept(tone(SampleRate / 2))
	time.Sleep(50 * time.Millisecond)

	// The utterance ends and the session resets for the next one.
	w.Reset()

	// The in-flight read now completes.
	time.Sleep(500 * time.Millisecond)

	if got := w.Partial(); got != "" {
		t.Errorf("Partial() = %q after Reset; text from the previous utterance leaked into this one", got)
	}
}

// The same hazard at Close: a partial that lands after the recognizer is
// closed has nowhere to go.
func TestPartialInFlightDoesNotOutliveClose(t *testing.T) {
	f := newFakeEngine(t, func(int64) string { return "late" })
	f.delay.Store(int64(200 * time.Millisecond))

	w := &whisperRecognizer{proc: f.proc}
	w.Accept(tone(SampleRate / 2))
	time.Sleep(30 * time.Millisecond)
	w.Close() // waits for the goroutine

	if got := w.Partial(); got != "" {
		t.Errorf("Partial() = %q after Close", got)
	}
}

// An utterance cannot grow without limit. Continuous sound — a noisy
// room, a fan, a mic left open next to a conversation — never produces
// the silence the endpoint needs, so nothing would ever commit and every
// partial would re-send an ever-larger recording.
func TestUtteranceIsBounded(t *testing.T) {
	f := newFakeEngine(t, func(int64) string { return "still going" })
	w := &whisperRecognizer{proc: f.proc}
	defer w.Close()

	// Two minutes of unbroken sound, in the chunks a browser sends.
	chunk := tone(SampleRate / 10)
	ended := false
	for i := 0; i < 1200; i++ {
		w.Accept(chunk)
		if w.Endpoint() {
			ended = true
			w.Reset()
		}
	}
	if !ended {
		t.Error("two minutes of continuous sound never ended an utterance")
	}

	w.mu.Lock()
	held := len(w.audio)
	w.mu.Unlock()
	if maxHeld := maxUtterance + SampleRate; held > maxHeld {
		t.Errorf("holding %.1fs of audio, more than the %.1fs cap",
			float64(held)/SampleRate, float64(maxHeld)/SampleRate)
	}
}
