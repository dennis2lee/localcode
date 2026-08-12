package dictation

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// asrServer imitates the server that produced this bug: a WhisperX ASR
// API, which answers /inference — the only endpoint localcode used to know
// — with 404, and serves the audio under a differently named form field.
func asrServer(t *testing.T, paths map[string]string, fileField string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var tried []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tried = append(tried, r.URL.Path)
		mu.Unlock()

		body, ok := paths[r.URL.Path]
		if !ok {
			// FastAPI's own 404 shape, which is what WhisperX serves.
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"detail":"Not Found"}`)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"detail":"bad form: %v"}`, err)
			return
		}
		if _, _, err := r.FormFile(fileField); err != nil {
			// A real server rejects the request without saying which field
			// it wanted — the error that makes this hard to diagnose.
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"detail":[{"loc":["body","`+fileField+`"],"msg":"field required"}]}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)

	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), tried...)
	}
}

func remoteProcess(t *testing.T, srv *httptest.Server) *whisperProcess {
	t.Helper()
	return &whisperProcess{host: strings.TrimPrefix(srv.URL, "http://"), log: &syncBuffer{}}
}

var testAudio = make([]float32, 16000) // one second of silence is still a request

// The reported failure: dictation ran, sent audio every 900ms, and nothing
// ever appeared, because the only endpoint localcode knew was one this
// server answers with 404.
func TestRemoteServerSpeakingOnlyOpenAIIsFound(t *testing.T) {
	srv, tried := asrServer(t, map[string]string{
		"/v1/audio/transcriptions": `{"text":"안녕하세요"}`,
	}, "file")
	p := remoteProcess(t, srv)

	got, err := p.transcribe(context.Background(), testAudio, "ko")
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if got != "안녕하세요" {
		t.Errorf("text = %q", got)
	}
	if len(tried()) != 1 {
		t.Errorf("tried %v, want the OpenAI endpoint first", tried())
	}
}

// WhisperX's native endpoint, which needs both a different path and a
// different form field. Getting only one of the two right produces a
// validation error rather than a transcript.
func TestRemoteServerSpeakingOnlyWhisperXIsFound(t *testing.T) {
	srv, tried := asrServer(t, map[string]string{
		"/asr": `{"text":"hello there"}`,
	}, "audio_file")
	p := remoteProcess(t, srv)

	got, err := p.transcribe(context.Background(), testAudio, "en")
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if got != "hello there" {
		t.Errorf("text = %q", got)
	}
	// It had to walk past the two it does not serve.
	if n := len(tried()); n != 3 {
		t.Errorf("tried %v, want all three", tried())
	}
}

// whisper.cpp, the locally spawned engine, must keep working exactly as
// before — this is the overwhelmingly common case.
func TestWhisperCppStillWorks(t *testing.T) {
	srv, _ := asrServer(t, map[string]string{"/inference": `{"text":"local engine"}`}, "file")
	p := remoteProcess(t, srv)

	got, err := p.transcribe(context.Background(), testAudio, "auto")
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if got != "local engine" {
		t.Errorf("text = %q", got)
	}
}

// The dialect is settled once and reused: discovery costs a couple of
// extra round trips on the first utterance and none after it.
func TestTheDialectIsFoundOnceAndRemembered(t *testing.T) {
	srv, tried := asrServer(t, map[string]string{"/asr": `{"text":"x"}`}, "audio_file")
	p := remoteProcess(t, srv)

	for range 3 {
		if _, err := p.transcribe(context.Background(), testAudio, "en"); err != nil {
			t.Fatalf("transcribe: %v", err)
		}
	}
	// 3 for the first (two misses plus the hit), then 1 each.
	if n := len(tried()); n != 5 {
		t.Errorf("%d requests for three utterances (%v); the dialect was re-discovered", n, tried())
	}
}

// A server that has the endpoint and rejects the request is answering the
// question that was asked. Treating that as "wrong endpoint" would send
// the search on to the next path and turn one clear error into three
// confusing ones.
func TestARealRejectionStopsTheSearch(t *testing.T) {
	srv, tried := asrServer(t, map[string]string{
		// Present, but wants a field the OpenAI dialect does not send.
		"/v1/audio/transcriptions": `{"text":"never reached"}`,
	}, "audio_file")
	p := remoteProcess(t, srv)

	_, err := p.transcribe(context.Background(), testAudio, "en")
	if err == nil {
		t.Fatal("a 422 was treated as success")
	}
	if len(tried()) != 1 {
		t.Errorf("tried %v, want to stop at the endpoint that answered", tried())
	}
	if !strings.Contains(err.Error(), "422") {
		t.Errorf("error = %v, want it to carry what the server said", err)
	}
}

// A server with none of them says so, naming what was tried — the failure
// that used to be a silent 404 every 900ms.
func TestAServerWithNoKnownEndpointSaysSo(t *testing.T) {
	srv, _ := asrServer(t, map[string]string{}, "file")
	p := remoteProcess(t, srv)

	_, err := p.transcribe(context.Background(), testAudio, "en")
	if err == nil {
		t.Fatal("no error from a server with no transcription endpoint at all")
	}
	for _, want := range []string{"openai", "whispercpp", "whisperx"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q: %v", want, err)
		}
	}
}

// The shapes these servers answer in. A transcript that comes back under a
// key nobody reads is indistinguishable from silence.
func TestParseTranscriptReadsEveryShape(t *testing.T) {
	cases := map[string]string{
		`{"text":"a"}`:                              "a",
		`{"transcription":"b"}`:                     "b",
		`{"segments":[{"text":"c "},{"text":"d"}]}`: "c d",
		`{"text":""}`:                               "",
	}
	for raw, want := range cases {
		got, err := parseTranscript([]byte(raw))
		if err != nil {
			t.Errorf("%s: %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("%s -> %q, want %q", raw, got, want)
		}
	}

	for _, raw := range []string{`{"error":"boom"}`, `{"detail":"boom"}`} {
		if _, err := parseTranscript([]byte(raw)); err == nil {
			t.Errorf("%s was not reported as an error", raw)
		}
	}
}

// A server that refuses everything must say so, once. This is the other
// half of the reported bug: even with the 404 fixed, a future mismatch
// would have been just as silent, because every failure path deliberately
// swallowed its error.
func TestAFailingServerIsReportedOnce(t *testing.T) {
	srv, _ := asrServer(t, map[string]string{}, "file")

	cfg := Config{Engine: EngineWhisper, WhisperURL: srv.URL, Language: "en"}
	rec, err := Open(cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer rec.Close()

	sess := NewSession(rec)
	if _, err := sess.Write(pcm16(tone(SampleRate))); err != nil {
		t.Fatal(err)
	}
	sess.Stop()

	reporter, ok := rec.(errorReporter)
	if !ok {
		t.Fatal("the whisper recognizer cannot report why it produced nothing")
	}
	msg := reporter.TakeError()
	if msg == "" {
		t.Fatal("a server that refused every request reported nothing at all")
	}
	if !strings.Contains(msg, "openai") {
		t.Errorf("the message does not say what was tried: %q", msg)
	}
	// Reported once: a persistent fault must not fill the transcript with
	// the same line four times a second.
	if again := reporter.TakeError(); again != "" {
		t.Errorf("the same failure was reported twice: %q", again)
	}
}
