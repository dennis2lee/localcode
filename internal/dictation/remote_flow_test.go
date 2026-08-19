package dictation

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The whole remote path, from the manager down: a session opened against a
// server on another machine, fed the audio a browser would send, and asked
// for the text.
//
// Every other test here drives whisperProcess directly, which is where the
// dialects live and is not where the reported failure was. "No error and
// no text" is a statement about what came back from Session.Write and
// Session.Stop, and nothing covered that against an engine that takes
// seconds to answer — which is the only thing a remote engine ever is.

// speech returns d worth of audio loud enough for the VAD to call speech.
func speech(d time.Duration) []float32 {
	out := make([]float32, durSamples(d))
	for i := range out {
		out[i] = 0.2 * float32(math.Sin(float64(i)*0.1))
	}
	return out
}

// quiet returns d worth of silence, which is how an utterance ends.
func quiet(d time.Duration) []float32 { return make([]float32, durSamples(d)) }

// pcmOf encodes samples the way the browser sends them.
func pcmOf(samples []float32) []byte {
	out := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(int16(s*32767)))
	}
	return out
}

// slowServer is a speech server on another machine: one dialect, 404 for
// the rest, and every answer takes a while. The delay is the whole point —
// a server that answers instantly is a local engine, and against a local
// engine none of this went wrong.
//
// The first request gets a different answer from the rest, because the
// first request is the preview of an utterance and the ones after it are
// the transcription of the finished thing. Telling them apart is what
// makes it possible to say *which* of the two ended up in the box.
func slowServer(t *testing.T, delay time.Duration, preview, final string, finalStatus int) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"detail":"Not Found"}`)
			return
		}
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"detail":"bad form: %v"}`, err)
			return
		}
		// The dialect probe is not an utterance: it is half a second of
		// silence sent to find out which endpoint this server has (see
		// ensureAPI), and it must not be counted as the preview below or
		// answered slowly — a real engine's cost is proportional to the
		// audio, and half a second of it is cheap however slow the machine.
		if isSilence(r) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"text":""}`)
			return
		}
		mu.Lock()
		n++
		first := n == 1
		mu.Unlock()
		time.Sleep(delay)
		if first {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"text":%q}`, preview)
			return
		}
		if finalStatus != http.StatusOK {
			w.WriteHeader(finalStatus)
			fmt.Fprint(w, `{"error":"model not loaded"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"text":%q}`, final)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// isSilence recognises the dialect probe: half a second of pure silence,
// sent to find out which endpoint this server has (see ensureAPI) rather
// than to transcribe anything.
//
// Recognised by its contents rather than its size, because size does not
// separate them — the first preview of an utterance is a quarter of a
// second of audio, which is smaller than the probe. Everything this test
// calls speech is non-zero.
func isSilence(r *http.Request) bool {
	f, _, err := r.FormFile("file")
	if err != nil {
		return false
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil || len(raw) <= 44 {
		return false
	}
	for _, b := range raw[44:] {
		if b != 0 {
			return false
		}
	}
	return true
}

func remoteSession(t *testing.T, srv *httptest.Server) *Session {
	t.Helper()
	m := NewManager(Config{
		Engine:     EngineWhisper,
		WhisperURL: strings.TrimPrefix(srv.URL, "http://"),
	})
	id, err := m.Start()
	if err != nil {
		t.Fatalf("start dictation: %v", err)
	}
	sess, err := m.Get(id)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	return sess
}

// speak feeds audio a quarter of a second at a time, in real time, the way
// the browser does, and collects everything the session hands back.
func speak(t *testing.T, sess *Session, audio []float32) (finals, errs []string) {
	t.Helper()
	step := durSamples(250 * time.Millisecond)
	for off := 0; off < len(audio); off += step {
		end := off + step
		if end > len(audio) {
			end = len(audio)
		}
		res, err := sess.Write(context.Background(), pcmOf(audio[off:end]))
		if err != nil {
			t.Fatalf("write audio: %v", err)
		}
		if res.Final != "" {
			finals = append(finals, res.Final)
		}
		if res.Error != "" {
			errs = append(errs, res.Error)
		}
		time.Sleep(250 * time.Millisecond)
	}
	return finals, errs
}

// The report, exactly: an engine on another machine, a sentence spoken
// into it, the microphone switched off, and nothing at all in the box.
//
// The cause was Stop cancelling the transcription it was about to wait for
// — so the sentence in flight died the moment the microphone went off, and
// with a local engine, which commits within a chunk or two, this never
// showed.
func TestTheLastSentenceSurvivesSwitchingTheMicrophoneOff(t *testing.T) {
	srv := slowServer(t, 1200*time.Millisecond, "half a sen", "the last thing I said", http.StatusOK)
	sess := remoteSession(t, srv)

	// Spoken, then a pause long enough to end the utterance, and the
	// microphone goes off while the engine is still working on it.
	finals, errs := speak(t, sess, append(speech(1200*time.Millisecond), quiet(1200*time.Millisecond)...))
	res := sess.Stop()
	if res.Final != "" {
		finals = append(finals, res.Final)
	}

	if len(errs) > 0 {
		t.Fatalf("dictation reported errors: %q", errs)
	}
	got := strings.Join(finals, " ")
	if !strings.Contains(got, "the last thing I said") {
		t.Fatalf("dictation produced %q, want the transcription of the whole sentence", got)
	}
}

// And a failure at that moment is reported, rather than joining it in the
// silence: Stop's answer used to carry text and no error, so a dictation
// whose only failure was its last one finished with nothing said at all.
func TestAFailureWhileStoppingIsReported(t *testing.T) {
	srv := slowServer(t, 1200*time.Millisecond, "half a sen", "", http.StatusInternalServerError)
	sess := remoteSession(t, srv)

	if _, errs := speak(t, sess, append(speech(1200*time.Millisecond), quiet(1200*time.Millisecond)...)); len(errs) > 0 {
		t.Fatalf("the failure was meant to happen at the end, not during: %q", errs)
	}
	if res := sess.Stop(); res.Error == "" {
		t.Fatal("the transcription failed as the microphone went off and nothing was said about it")
	}
}

// A transcription that fails is not a sentence thrown away: the last
// preview of the same audio is imperfect and is what the person said.
func TestAFailedFinalFallsBackToThePreviewOfIt(t *testing.T) {
	srv := slowServer(t, 1200*time.Millisecond, "half a sen", "", http.StatusInternalServerError)
	sess := remoteSession(t, srv)

	finals, _ := speak(t, sess, append(speech(1200*time.Millisecond), quiet(1200*time.Millisecond)...))
	if res := sess.Stop(); res.Final != "" {
		finals = append(finals, res.Final)
	}
	if got := strings.Join(finals, " "); !strings.Contains(got, "half a sen") {
		t.Fatalf("dictation produced %q, want the preview it already had", got)
	}
}

// Switching the microphone off is not a failure, and must not be reported
// as one.
//
// Stop cancels the previews in flight — that is what makes the button feel
// immediate — and every one of those cancellations came back as an error
// the session then reported. So the ordinary, correct end of every remote
// dictation printed a line of red under a perfectly good transcript:
//
//	whisper engine: Post "http://10.0.0.24:8123/inference": context canceled
//
// which, to someone who has already been told dictation is broken, is the
// evidence that it still is.
func TestSwitchingTheMicrophoneOffIsNotAnError(t *testing.T) {
	// Slow enough that the preview started at the first chunk is certainly
	// still in flight when the microphone goes off — which is the whole
	// point: it is that preview's cancellation that used to be reported.
	srv := slowServer(t, 3*time.Second, "half a sen", "the whole sentence", http.StatusOK)
	sess := remoteSession(t, srv)

	// Stopped mid-sentence, with a preview certainly still in flight.
	if _, errs := speak(t, sess, speech(1200*time.Millisecond)); len(errs) > 0 {
		t.Fatalf("nothing had failed yet: %q", errs)
	}
	if res := sess.Stop(); res.Error != "" {
		t.Fatalf("stopping reported %q; cancelling our own previews is not a failure", res.Error)
	}
}

// Sentences reach the prompt box in the order they were spoken.
//
// Each utterance is transcribed by its own request, and the requests
// overlap on any engine slow enough to matter — which is every engine on
// another machine. A short second sentence then overtakes a long first
// one, and what arrived in the box was the two of them back to front, with
// nothing able to put them right afterwards.
func TestSentencesArriveInTheOrderTheyWereSpoken(t *testing.T) {
	// Cost proportional to the audio, the way a real engine's is, and the
	// text says how long the recording was — so the order is checkable.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		raw, _ := io.ReadAll(f)
		seconds := float64(len(raw)-44) / (2 * SampleRate)
		// A long recording costs real time and a short one is quick, which
		// is what lets the second sentence overtake the first.
		if seconds > 2 {
			time.Sleep(3 * time.Second)
		} else {
			time.Sleep(200 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"text":"sentence of %.0f"}`, seconds*10)
	}))
	t.Cleanup(srv.Close)
	sess := remoteSession(t, &httptest.Server{URL: srv.URL, Config: srv.Config})

	// A long sentence, then a short one: the short one finishes first.
	var audio []float32
	audio = append(audio, speech(1200*time.Millisecond)...)
	audio = append(audio, quiet(1400*time.Millisecond)...)
	audio = append(audio, speech(400*time.Millisecond)...)
	audio = append(audio, quiet(1400*time.Millisecond)...)
	finals, _ := speak(t, sess, audio)
	if res := sess.Stop(); res.Final != "" {
		finals = append(finals, res.Final)
	}

	// Each sentence names how long its recording was, so the order they
	// arrived in is readable: the first one spoken is the longer of the two.
	got := strings.Join(finals, " ")
	var lengths []int
	for _, m := range sentenceLength.FindAllStringSubmatch(got, -1) {
		n, _ := strconv.Atoi(m[1])
		lengths = append(lengths, n)
	}
	if len(lengths) != 2 {
		t.Fatalf("dictation produced %q, want both sentences", got)
	}
	if lengths[0] < lengths[1] {
		t.Errorf("dictation produced %q; the sentences came back in the order the engine finished them, not the order they were spoken", got)
	}
}

var sentenceLength = regexp.MustCompile(`sentence of (\d+)`)
