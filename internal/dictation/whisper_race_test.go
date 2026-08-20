package dictation

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	f.proc = &whisperProcess{host: net.JoinHostPort("127.0.0.1", portStr)}
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

// A remote engine is driven exactly like a local one, minus the process.
// This is the whole feature: point at another machine, install nothing
// here, and dictation works.
func TestRemoteEngineTranscribesWithNothingInstalledLocally(t *testing.T) {
	// Behaves like whisper.cpp's own server: /inference and nothing else.
	// It used to answer every path, which stopped being a fair imitation
	// once localcode learned to look for more than one — a server that
	// says yes to everything settles on whichever is tried first, which
	// says nothing about whether the right one was found.
	// Under a lock because these no longer all arrive on one connection:
	// the streaming attempt that now precedes them is a connection of its
	// own, and net/http serves each on its own goroutine.
	var askedMu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		askedMu.Lock()
		asked = append(asked, r.URL.Path)
		askedMu.Unlock()
		if r.URL.Path != "/inference" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"text":" 안녕하세요\n"}`)
	}))
	defer srv.Close()

	// No WhisperBin, no WhisperModel: nothing is installed on this side.
	cfg := Config{Engine: EngineWhisper, WhisperURL: srv.URL, Language: "ko"}
	rec, err := Open(cfg)
	if err != nil {
		t.Fatalf("opening a remote engine: %v", err)
	}
	defer rec.Close()

	sess := NewSession(rec)
	if _, err := sess.Write(context.Background(), pcm16(tone(SampleRate))); err != nil {
		t.Fatal(err)
	}
	if got := sess.Stop().Final; got != "안녕하세요" {
		t.Errorf("final = %q, want %q", got, "안녕하세요")
	}
	// And it found the endpoint this server actually serves, rather than
	// giving up on the first 404 the way it did before.
	var hit bool
	askedMu.Lock()
	defer askedMu.Unlock()
	for _, path := range asked {
		if path == "/inference" {
			hit = true
		}
	}
	if !hit {
		t.Errorf("never asked /inference; tried %v", asked)
	}
}

// A wrong address must fail when dictation starts, naming the address —
// not as silence the first time someone speaks, which is exactly what a
// broken microphone looks like.
func TestAnUnreachableRemoteEngineFailsWithItsAddress(t *testing.T) {
	// Port 1 on loopback: nothing listens there, and the connection is
	// refused immediately rather than hanging.
	_, err := Open(Config{Engine: EngineWhisper, WhisperURL: "127.0.0.1:1"})
	if err == nil {
		t.Fatal("opening an unreachable remote engine succeeded")
	}
	if !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("the error does not name the address: %v", err)
	}
}

// Closing a remote recognizer must not try to kill anything: there is no
// child process, and Shutdown running over it would be reaching into
// another machine's business.
func TestClosingARemoteEngineIsHarmless(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"text":"ok"}`)
	}))
	defer srv.Close()

	rec, err := Open(Config{Engine: EngineWhisper, WhisperURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	rec.Close()
	Shutdown() // must not panic, and must not touch the remote

	// Still serving: nothing was killed.
	if _, err := Open(Config{Engine: EngineWhisper, WhisperURL: srv.URL}); err != nil {
		t.Errorf("the remote engine became unusable after Close/Shutdown: %v", err)
	}
}
