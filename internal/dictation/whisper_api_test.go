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

// resettingServer accepts the connection and then closes it without
// answering, for the paths named. That is what "an existing connection was
// forcibly closed by the remote host" looks like from the other side, and
// it is what a server does when it decides to reject a request and does
// not wait to read the body first.
func resettingServer(t *testing.T, reset map[string]bool, serve map[string]string, fileField string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var tried []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tried = append(tried, r.URL.Path)
		mu.Unlock()

		if reset[r.URL.Path] {
			// Hijack and close: no status line, no body, connection gone.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		body, ok := serve[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"detail":"Not Found"}`)
			return
		}
		// Only an upload carries a form. A plain GET — which is how the
		// probe asks whether ordinary HTTP works at all — must be
		// answered as itself.
		if r.Method == http.MethodPost {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if _, _, err := r.FormFile(fileField); err != nil {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), tried...)
	}
}

// The bug this test exists for: a connection reset is not evidence about
// which endpoint the server has, and settling the dialect on it sent every
// later utterance to a path that was never going to answer — while the
// endpoint that does work went untried.
func TestAResetConnectionDoesNotSettleTheDialect(t *testing.T) {
	srv, tried := resettingServer(t,
		map[string]bool{"/v1/audio/transcriptions": true},
		map[string]string{"/asr": `{"text":"found the working one"}`},
		"audio_file")
	p := remoteProcess(t, srv)

	got, err := p.transcribe(context.Background(), testAudio, "ko")
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if got != "found the working one" {
		t.Errorf("text = %q", got)
	}
	paths := tried()
	if len(paths) < 3 {
		t.Fatalf("stopped after %v; a reset must not end the search", paths)
	}

	// And the dialect that settled is the one that answered.
	p.apiMu.Lock()
	settled := p.api.name
	p.apiMu.Unlock()
	if settled != "whisperx" {
		t.Errorf("settled on %q, want the endpoint that actually replied", settled)
	}
}

// A server that is entirely unreachable is a different thing from a server
// missing these endpoints, and saying the wrong one sends someone to look
// in the wrong place. It also must not settle anything: a network that
// dropped for a second must not disable dictation for the rest of the run.
func TestAnUnreachableServerIsReportedAsUnreachable(t *testing.T) {
	srv, _ := resettingServer(t,
		map[string]bool{"/v1/audio/transcriptions": true, "/inference": true, "/asr": true},
		nil, "file")
	p := remoteProcess(t, srv)

	_, err := p.transcribe(context.Background(), testAudio, "ko")
	if err == nil {
		t.Fatal("a server that reset every connection reported success")
	}
	if !strings.Contains(err.Error(), "could not reach") {
		t.Errorf("error = %v, want it to say the server could not be reached", err)
	}

	p.apiMu.Lock()
	settled := p.apiSettled
	p.apiMu.Unlock()
	if settled {
		t.Error("a dialect was settled from requests that never got an answer")
	}
}

// The probe exists to separate three failures that look identical from
// the prompt box: a wrong address, an endpoint that is not there, and
// HTTP working while the upload specifically is dropped. The last is the
// one that has no other symptom — every candidate fails the same way and
// the transcript names only the first.
func TestProbeSeparatesADroppedUploadFromAMissingEndpoint(t *testing.T) {
	// GETs answer; every POST is reset. This is the shape that had us
	// chasing endpoints when the endpoints were never the problem.
	srv, _ := resettingServer(t,
		map[string]bool{"/v1/audio/transcriptions": true, "/inference": true, "/asr": true},
		map[string]string{"/": `{"status":"running"}`, "/health": `{"status":"healthy"}`},
		"file")

	res, err := Probe(context.Background(), Config{WhisperURL: srv.URL})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	byWhat := map[string]ProbeStep{}
	for _, s := range res.Steps {
		byWhat[s.What] = s
	}
	if !byWhat["tcp"].OK {
		t.Error("tcp should connect")
	}
	if !byWhat["GET /"].OK {
		t.Errorf("GET / = %s %s, want it to answer", byWhat["GET /"].Status, byWhat["GET /"].Detail)
	}
	for _, api := range whisperAPIs {
		s := byWhat["POST "+api.path+" ("+api.name+")"]
		if s.Status != "no reply" {
			t.Errorf("%s = %q, want it reported as getting no reply", s.What, s.Status)
		}
	}
	if !strings.Contains(res.Summary(), "closes the connection on every upload") {
		t.Errorf("summary does not name what is actually happening: %s", res.Summary())
	}
}

// And when an endpoint does work, the probe says so plainly rather than
// leaving someone to read four lines and infer it.
func TestProbeReportsAWorkingEndpoint(t *testing.T) {
	srv, _ := asrServer(t, map[string]string{
		"/":    `{"status":"running"}`,
		"/asr": `{"text":"heard you"}`,
	}, "audio_file")

	res, err := Probe(context.Background(), Config{WhisperURL: srv.URL})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !strings.Contains(res.Summary(), "should work") {
		t.Errorf("summary = %s", res.Summary())
	}
}

// The dead end this turns into an instruction: a TLS port addressed as
// plain http closes every connection without answering, which is
// indistinguishable from a server refusing the request — TCP connects,
// nothing else works, and nothing says why.
func TestProbeIdentifiesAPortThatSpeaksTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"running"}`)
	}))
	defer srv.Close()

	// Configured as http://, which is what makes it fail.
	plain := strings.Replace(srv.URL, "https://", "http://", 1)
	res, err := Probe(context.Background(), Config{WhisperURL: plain})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	var tlsStep ProbeStep
	for _, s := range res.Steps {
		if s.What == "TLS handshake" {
			tlsStep = s
		}
	}
	if !tlsStep.OK {
		t.Fatalf("a TLS port was not identified as one: %+v", res.Steps)
	}
	if !strings.Contains(res.Summary(), "https://") {
		t.Errorf("the summary does not say what to change: %s", res.Summary())
	}
}

// And with https:// configured, the same server just works — the scheme
// used to be discarded, so an https address was silently sent as http.
func TestAnHTTPSServerIsReachedWhenConfiguredAsSuch(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"running"}`)
	}))
	defer srv.Close()

	cfg := Config{WhisperURL: srv.URL}
	if got := cfg.RemoteScheme(); got != "https" {
		t.Fatalf("RemoteScheme() = %q, want https — the scheme is being thrown away", got)
	}
}
