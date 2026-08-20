package dictation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// What these cover: dictation against a server that can transcribe while
// someone is still talking.
//
// The HTTP path re-sends the whole utterance every 900ms and shows the
// answer to the previous one; over a network that is a second of lag and
// a growing upload per sentence. Streaming replaces both, and the things
// that can go wrong when it does are all about boundaries — text that
// arrives before the pause, text that arrives after it, and the same
// sentence being committed twice because the server keeps sending it.

// fakeLive is a stand-in WhisperLive server: it completes the handshake,
// says SERVER_READY, and then hands the connection to talk. Returns the
// host:port to configure as whisper_url.
func fakeLive(t *testing.T, talk func(c *wsConn, opts map[string]any)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := upgrade(t, w, r)
		defer c.Close()
		_, hello, err := c.read(time.Now().Add(5 * time.Second))
		if err != nil {
			return
		}
		var opts map[string]any
		if err := json.Unmarshal(hello, &opts); err != nil {
			c.writeText(`{"status":"ERROR","message":"unreadable options"}`)
			return
		}
		if err := c.writeText(`{"uid":"test","message":"SERVER_READY","backend":"faster_whisper"}`); err != nil {
			return
		}
		talk(c, opts)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// segment is one entry of the message WhisperLive sends back. The times
// are strings because that is how it formats them.
func segment(start float64, text string, done bool) string {
	return fmt.Sprintf(`{"start":"%.3f","end":"%.3f","text":%q,"completed":%t}`,
		start, start+1, text, done)
}

func segments(segs ...string) string {
	return `{"uid":"test","segments":[` + strings.Join(segs, ",") + `]}`
}

// drain reads audio frames until the client goes away, counting samples,
// and calls on each frame with the running total in seconds.
func drain(c *wsConn, on func(seconds float64) string) {
	var samples int
	for {
		op, msg, err := c.read(time.Time{})
		if err != nil {
			return
		}
		if op != opBinary {
			continue
		}
		samples += len(msg) / 4 // float32 on the wire
		if reply := on(float64(samples) / SampleRate); reply != "" {
			if err := c.writeText(reply); err != nil {
				return
			}
		}
	}
}

func liveSession(t *testing.T, host string) *Session {
	t.Helper()
	m := NewManager(Config{Engine: EngineWhisper, WhisperURL: host})
	id, err := m.Start()
	if err != nil {
		t.Fatalf("start dictation: %v", err)
	}
	sess, err := m.Get(id)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	t.Cleanup(func() { m.Stop(id) })
	return sess
}

// The point of the whole feature: text on screen while the sentence is
// still being spoken, with no pause and no second request.
func TestTextArrivesWhileTheSentenceIsStillBeingSpoken(t *testing.T) {
	t.Parallel()
	host := fakeLive(t, func(c *wsConn, _ map[string]any) {
		drain(c, func(sec float64) string {
			if sec < 0.4 {
				return ""
			}
			return segments(segment(0, " 안녕하세요", false))
		})
	})

	sess := liveSession(t, host)
	// One second of speech and no silence at all: nothing here has ended,
	// so on the HTTP path there would be nothing to show yet.
	deadline := time.Now().Add(5 * time.Second)
	var provisional string
	for time.Now().Before(deadline) && provisional == "" {
		res, err := sess.Write(context.Background(), pcmOf(speech(250*time.Millisecond)))
		if err != nil {
			t.Fatal(err)
		}
		provisional = res.Provisional
		if res.Error != "" {
			t.Fatalf("dictation reported %q", res.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if provisional != "안녕하세요" {
		t.Fatalf("provisional = %q, want 안녕하세요", provisional)
	}
}

// A streaming server holds one transcript for the whole connection and
// keeps re-sending the tail of it. This side commits a sentence when the
// speaker pauses, so without a line drawn at that point the same words
// arrive again a moment later and are typed into the prompt box twice.
func TestASentenceIsCommittedOnceEvenThoughTheServerKeepsSendingIt(t *testing.T) {
	t.Parallel()
	host := fakeLive(t, func(c *wsConn, _ map[string]any) {
		drain(c, func(sec float64) string {
			// The first sentence, forever, plus a second one once enough
			// audio has gone by for it to have been spoken.
			if sec > 3.5 {
				return segments(segment(0, " 첫 문장", true), segment(3, " 둘째 문장", true))
			}
			return segments(segment(0, " 첫 문장", true))
		})
	})

	sess := liveSession(t, host)
	audio := append(speech(1200*time.Millisecond), quiet(1500*time.Millisecond)...)
	audio = append(audio, speech(1200*time.Millisecond)...)
	audio = append(audio, quiet(1500*time.Millisecond)...)
	finals, errs := speak(t, sess, audio)
	if len(errs) > 0 {
		t.Fatalf("dictation reported %q", errs)
	}
	rest := sess.Stop()
	if rest.Final != "" {
		finals = append(finals, rest.Final)
	}

	got := strings.Join(finals, " | ")
	if got != "첫 문장 | 둘째 문장" {
		t.Fatalf("committed %q, want \"첫 문장 | 둘째 문장\" — a sentence was dropped or typed twice", got)
	}
}

// The spoken language has to reach the server, or English speech comes
// back written in whatever the server guessed. It is in the handshake
// here, not in a form field, and nothing else sends it.
func TestTheHandshakeCarriesTheSpokenLanguage(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var got map[string]any
	host := fakeLive(t, func(c *wsConn, opts map[string]any) {
		mu.Lock()
		got = opts
		mu.Unlock()
		drain(c, func(float64) string { return "" })
	})

	rec, err := openWhisper(Config{Engine: EngineWhisper, WhisperURL: host, Language: "en"})
	if err != nil {
		t.Fatalf("openWhisper: %v", err)
	}
	defer rec.Close()
	if _, ok := rec.(*streamRecognizer); !ok {
		t.Fatalf("got %T, want the streaming recognizer", rec)
	}

	mu.Lock()
	defer mu.Unlock()
	if got["language"] != "en" {
		t.Errorf("language = %v, want en", got["language"])
	}
	if got["task"] != "transcribe" {
		t.Errorf("task = %v, want transcribe — a server set to translate would rewrite what was said", got["task"])
	}
	if got["model"] == nil {
		t.Error("no model in the handshake: the server reads it directly and drops the connection without one")
	}
}

// Most speech servers do not stream, and the attempt to find out must
// cost them nothing: an HTTP-only server has to go on working exactly as
// it did, transcribing over HTTP, with no error shown to anyone.
func TestAServerThatOnlySpeaksHTTPIsStillUsedOverHTTP(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inference" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"text":"http still works"}`)
	}))
	defer srv.Close()

	rec, err := openWhisper(Config{Engine: EngineWhisper, WhisperURL: srv.URL})
	if err != nil {
		t.Fatalf("openWhisper: %v", err)
	}
	defer rec.Close()
	w, ok := rec.(*whisperRecognizer)
	if !ok {
		t.Fatalf("got %T, want the HTTP recognizer", rec)
	}
	w.Accept(speech(time.Second))
	if text := w.Final(context.Background()); text != "http still works" {
		t.Errorf("Final = %q, want the HTTP transcript", text)
	}
}

// Naming the protocol is how someone says "this server does stream, stop
// guessing". Then it has to fail out loud: falling back to HTTP would be
// slower than what was asked for and silent about it.
func TestANamedStreamingServerThatIsNotThereFailsOutLoud(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(http.NotFound))
	defer srv.Close()

	rec, err := openWhisper(Config{Engine: EngineWhisper, WhisperURL: srv.URL, WhisperAPI: "whisperlive"})
	if err == nil {
		rec.Close()
		t.Fatal("a pinned streaming server that is not there was accepted")
	}
	if !strings.Contains(err.Error(), "websocket") {
		t.Errorf("error %q does not say what was wrong", err)
	}
}

// The audio is sent as it is spoken, so when the speaker stops there is
// always a little of it the server has not read yet. Committing without
// waiting for it takes the last words off every sentence.
func TestTheEndOfASentenceIsWaitedForBeforeItIsCommitted(t *testing.T) {
	t.Parallel()
	host := fakeLive(t, func(c *wsConn, _ map[string]any) {
		// The tail is sent 400ms after the last audio arrives, which is
		// what a server still working through the end of an utterance
		// looks like: the client has stopped sending, and the words are
		// not back yet.
		var mu sync.Mutex
		var timer *time.Timer
		defer func() {
			mu.Lock()
			if timer != nil {
				timer.Stop()
			}
			mu.Unlock()
		}()
		drain(c, func(sec float64) string {
			if sec < 1.0 {
				return ""
			}
			mu.Lock()
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(400*time.Millisecond, func() {
				c.writeText(segments(segment(0, " 문장 전체입니다", true)))
			})
			mu.Unlock()
			return segments(segment(0, " 문장", false))
		})
	})

	sess := liveSession(t, host)
	finals, errs := speak(t, sess, append(speech(1200*time.Millisecond), quiet(1500*time.Millisecond)...))
	if len(errs) > 0 {
		t.Fatalf("dictation reported %q", errs)
	}
	if got := strings.Join(finals, " "); got != "문장 전체입니다" {
		t.Fatalf("committed %q, want the whole sentence", got)
	}
}

// `dictation probe` is what someone runs when nothing comes out, so its
// verdict has to be right about a streaming server — which is entitled
// to have no HTTP endpoints at all. Every upload line against one says
// 404, and reading that as "this server cannot transcribe" would send
// someone to reconfigure a server that works.
func TestTheProbeSaysAStreamingServerWorks(t *testing.T) {
	t.Parallel()
	host := fakeLive(t, func(c *wsConn, _ map[string]any) {
		drain(c, func(float64) string { return "" })
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := Probe(ctx, Config{Engine: EngineWhisper, WhisperURL: host})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	var streamed bool
	for _, step := range res.Steps {
		if strings.HasPrefix(step.What, "WS ") && step.OK {
			streamed = true
		}
	}
	if !streamed {
		t.Errorf("the probe did not find the streaming endpoint: %+v", res.Steps)
	}
	if !strings.Contains(res.Summary(), "stream") {
		t.Errorf("summary = %q, want it to say the server streams", res.Summary())
	}
}
