package dictation

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
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
	// Two requests: the probe that settles the dialect and the audio
	// itself. See ensureAPI for why the search is not run against the
	// utterance.
	if got := tried(); len(got) != 2 || got[0] != "/v1/audio/transcriptions" {
		t.Errorf("tried %v, want the OpenAI endpoint probed and then used", got)
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
	// It had to walk past the two it does not serve, and then send the
	// audio to the one that answered.
	if got := tried(); len(got) != 4 || got[3] != "/asr" {
		t.Errorf("tried %v, want all three probed and the audio sent to /asr", got)
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
	// Three probes to find it, once, and then one request per utterance.
	if n := len(tried()); n != 6 {
		t.Errorf("%d requests for three utterances (%v); the dialect was re-discovered", n, tried())
	}
}

// A server that has the endpoint and rejects the request is answering the
// question that was asked — so once the search has looked everywhere, that
// is the endpoint it settles on and that refusal is what gets reported.
// Treating it as "wrong endpoint" instead would turn one clear error into
// "this server has none of the endpoints localcode knows", which is a
// different and untrue claim.
//
// It is not chosen *while another path might work*, though: see
// TestAnEndpointThatRefusesDoesNotShadowOneThatWorks.
func TestARealRejectionIsWhatGetsReported(t *testing.T) {
	srv, _ := asrServer(t, map[string]string{
		// Present, but wants a field the OpenAI dialect does not send.
		"/v1/audio/transcriptions": `{"text":"never reached"}`,
	}, "audio_file")
	p := remoteProcess(t, srv)

	_, err := p.transcribe(context.Background(), testAudio, "en")
	if err == nil {
		t.Fatal("a 422 was treated as success")
	}
	if !strings.Contains(err.Error(), "422") {
		t.Errorf("error = %v, want it to carry what the server said", err)
	}
	if strings.Contains(err.Error(), "none of the transcription endpoints") {
		t.Errorf("error = %v, want the server's own refusal rather than a claim about its endpoints", err)
	}
}

// The refinement that costs the least and matters most on a WhisperX
// server, which serves an OpenAI-shaped endpoint *and* its own: if the
// first one refuses the request permanently — a missing field, a model it
// does not have — the search must not stop there. It used to, and the
// endpoint that would have transcribed every utterance was one path
// further down a list the search had already left.
func TestAnEndpointThatRefusesDoesNotShadowOneThatWorks(t *testing.T) {
	srv, tried := asrServer(t, map[string]string{
		// Both present. The OpenAI one wants a field that dialect does not
		// send, so it answers 422 forever; /asr works.
		"/v1/audio/transcriptions": `{"text":"never reached"}`,
		"/asr":                     `{"text":"hello there"}`,
	}, "audio_file")
	p := remoteProcess(t, srv)

	got, err := p.transcribe(context.Background(), testAudio, "en")
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if got != "hello there" {
		t.Errorf("text = %q, want the endpoint that works to have been found past the one that refuses", got)
	}
	if last := tried()[len(tried())-1]; last != "/asr" {
		t.Errorf("the audio went to %s, want /asr", last)
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
	if _, err := sess.Write(context.Background(), pcm16(tone(SampleRate))); err != nil {
		t.Fatal(err)
	}
	// Stopping reports the failure too, so it is taken from there rather
	// than from the recognizer: the last error of a dictation reaches the
	// user through the stop reply and nowhere else.
	msg := sess.Stop().Error

	reporter, ok := rec.(errorReporter)
	if !ok {
		t.Fatal("the whisper recognizer cannot report why it produced nothing")
	}
	if msg == "" {
		msg = reporter.TakeError()
	}
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

// A server that hangs on a path it does not serve, rather than answering
// 404. The search has to reach the endpoint that works: sharing one
// deadline let the first candidate spend all of it, the rest failed
// instantly on an expired context, and every later utterance repeated the
// same thing — dictation that produced nothing and never said why.
func TestADialectThatHangsDoesNotHideTheOneThatWorks(t *testing.T) {
	block := make(chan struct{})

	var mu sync.Mutex
	tried := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tried[r.URL.Path]++
		mu.Unlock()
		if r.URL.Path == "/inference" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"text":"안녕하세요"}`)
			return
		}
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	// Released before the server is closed: Close waits for handlers that
	// are still running, and these are the ones deliberately stuck.
	defer srv.Close()
	defer close(block)

	p := remoteProcess(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	text, err := p.transcribe(ctx, testAudio, "ko")
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if text != "안녕하세요" {
		t.Errorf("text = %q, want the answer from the endpoint that works", text)
	}
	mu.Lock()
	defer mu.Unlock()
	if tried["/inference"] == 0 {
		t.Error("the working endpoint was never tried")
	}
}

// The endpoint search must not be run against the sentence someone just
// said.
//
// This is the failure that sent someone to reconfigure a server that was
// working. The search used to send the real utterance to each candidate in
// turn, sharing that utterance's deadline between them — so on a server
// that takes a few seconds per utterance, the endpoint that would have
// transcribed it was cut off part-way through, and what appeared in the
// transcript was "10.0.0.24:8123 has none of the transcription endpoints
// localcode knows". The server had the endpoint. It was given a third of
// the time it needed.
//
// The invariant that fixes it: only the endpoint that answered ever
// receives the audio. Everything else is asked with half a second of
// silence, which is cheap on any engine whatever its speed.
func TestTheEndpointSearchNeverSpendsTheUtterance(t *testing.T) {
	var mu sync.Mutex
	sizes := map[string][]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(8 << 20); err == nil {
			for _, files := range r.MultipartForm.File {
				for _, f := range files {
					mu.Lock()
					sizes[r.URL.Path] = append(sizes[r.URL.Path], int(f.Size))
					mu.Unlock()
				}
			}
		}
		if r.URL.Path != "/asr" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"text":"the sentence"}`)
	}))
	t.Cleanup(srv.Close)
	p := remoteProcess(t, srv)

	utterance := make([]float32, 5*SampleRate) // five seconds, as a sentence is
	if _, err := p.transcribe(context.Background(), utterance, "en"); err != nil {
		t.Fatalf("transcribe: %v", err)
	}

	probeBytes := 44 + 2*(SampleRate/2)
	mu.Lock()
	defer mu.Unlock()
	for path, got := range sizes {
		for _, size := range got {
			if path == "/asr" && size > probeBytes {
				continue // the endpoint that answered, receiving the audio
			}
			if size > probeBytes {
				t.Errorf("%s was sent %d bytes of audio; the search must cost a probe, not the utterance", path, size)
			}
		}
	}
}

// A candidate that never answers proves nothing, and must not be reported
// as one that is absent. "This server has none of the endpoints localcode
// knows" is a different claim from "something did not reply", and only one
// of them is knowable here.
func TestACandidateThatNeverAnswersIsNotCalledMissing(t *testing.T) {
	// Released at the end of the test rather than left to the request's own
	// context: httptest.Server.Close waits for its handlers, and a handler
	// parked on a cancellation it may not be told about hangs the suite.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/audio/transcriptions" {
			w.WriteHeader(http.StatusNotFound) // this one really is absent
			return
		}
		select { // and these two say nothing at all
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	p := remoteProcess(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	_, err := p.transcribe(ctx, testAudio, "en")
	if err == nil {
		t.Fatal("a server that answered nothing was treated as working")
	}
	if strings.Contains(err.Error(), "none of the transcription endpoints") {
		t.Errorf("error = %v, want it to say the server did not answer rather than that it lacks the endpoints", err)
	}
	if !strings.Contains(err.Error(), "could not reach") {
		t.Errorf("error = %v, want it to name what actually happened", err)
	}
}

// The whisperX ASR service takes its options in the query string and
// ignores form fields it does not know. Sending the spoken language as a
// form field there is not an error anybody sees — the server auto-detects
// every utterance and the setting simply has no effect, which is exactly
// "I chose English and the text still comes back in Korean".
func TestWhisperXIsToldTheLanguageWhereItReadsIt(t *testing.T) {
	var query url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/asr" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"text":"hello there"}`)
	}))
	t.Cleanup(srv.Close)
	p := remoteProcess(t, srv)

	if _, err := p.transcribe(context.Background(), testAudio, "en"); err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if got := query.Get("language"); got != "en" {
		t.Errorf("language=%q in the query, want en", got)
	}
	// And the output format, for the same reason: this service's default
	// output is plain text, which is not something parseTranscript can read.
	if got := query.Get("output"); got != "json" {
		t.Errorf("output=%q in the query, want json", got)
	}
}

// "auto" is this package's word for "work it out", not a language. Sent to
// a server whose language parameter takes a code, it is a code for nothing.
func TestWhisperXIsSentNoLanguageWhenThereIsNone(t *testing.T) {
	var query url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/asr" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"text":"hello there"}`)
	}))
	t.Cleanup(srv.Close)
	p := remoteProcess(t, srv)

	if _, err := p.transcribe(context.Background(), testAudio, "auto"); err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if _, ok := query["language"]; ok {
		t.Errorf("language=%q was sent, want it omitted so the server auto-detects", query.Get("language"))
	}
}
