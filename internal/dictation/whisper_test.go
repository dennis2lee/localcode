package dictation

import (
	"bytes"
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Whisper narrates non-speech in brackets, and dictation is mostly
// non-speech: every pause to think would otherwise type "[BLANK_AUDIO]"
// into the prompt box.
func TestCleanTranscript(t *testing.T) {
	tests := []struct{ in, want string }{
		{" 그는 괜찮은 척 하려고 애쓰는 것 같았다\n", "그는 괜찮은 척 하려고 애쓰는 것 같았다"},
		{"[BLANK_AUDIO]", ""},
		{"[ blank audio ]", ""},
		{" [Music] hello there\n", "hello there"},
		{"（음악）안녕하세요", "안녕하세요"},
		{"  spaced   out  \n\n", "spaced out"},
		{"", ""},

		// Speech that happens to contain brackets is kept. Stripping
		// every bracketed group is the obvious rule and it deletes what
		// people actually said, with nothing on screen to show it went.
		{"이 함수(비동기)를 async로 바꿔줘", "이 함수(비동기)를 async로 바꿔줘"},
		{"retry(3회)로 설정해줘", "retry(3회)로 설정해줘"},
		{"the parser (recursive descent) needs a fix", "the parser (recursive descent) needs a fix"},

		// A reply that is only a bracketed group was an annotation under
		// a name this does not know: nobody dictates a lone parenthesis.
		{"(something odd)", ""},
	}
	for _, tc := range tests {
		if got := cleanTranscript(tc.in); got != tc.want {
			t.Errorf("cleanTranscript(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The WAV this builds is the only thing the engine ever sees, so a
// wrong header would look exactly like the model mishearing.
func TestWriteWAVRoundTrips(t *testing.T) {
	in := make([]float32, 1600)
	for i := range in {
		in[i] = float32(math.Sin(float64(i) * 0.05))
	}
	var buf bytes.Buffer
	if err := writeWAV(&buf, in); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "a.wav")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	// Read back with the strict reader dictation test uses: it rejects
	// anything not mono 16 kHz 16-bit by name.
	out, rate, err := ReadWAV(path)
	if err != nil {
		t.Fatalf("reading back what we wrote: %v", err)
	}
	if rate != SampleRate {
		t.Errorf("rate = %d, want %d", rate, SampleRate)
	}
	if len(out) != len(in) {
		t.Fatalf("got %d samples, wrote %d", len(out), len(in))
	}
	for i := range in {
		if math.Abs(float64(in[i]-out[i])) > 1e-4 {
			t.Fatalf("sample %d: got %v, wrote %v", i, out[i], in[i])
		}
	}
}

// The VAD is what decides when grey text becomes committed text, since
// whisper.cpp has no notion of a speaker stopping.
func TestVADEndpointsAfterSpeechThenSilence(t *testing.T) {
	var v vad

	if v.feed(silence(2 * SampleRate)) {
		t.Error("endpoint on silence alone, with nothing ever said")
	}
	if v.spoke() {
		t.Error("spoke = true after silence only")
	}

	if v.feed(tone(SampleRate)) {
		t.Error("endpoint while still speaking")
	}
	if !v.spoke() {
		t.Error("spoke = false after a second of speech")
	}

	// Less quiet than it takes to end an utterance.
	if v.feed(silence(SampleRate / 4)) {
		t.Error("endpoint after a quarter second of quiet, which is a pause between words")
	}
	if !v.feed(silence(SampleRate)) {
		t.Error("no endpoint after a second of quiet following speech")
	}

	v.reset()
	if v.spoke() || v.feed(nil) {
		t.Error("reset left state behind")
	}
}

// A cough is not an utterance.
func TestVADIgnoresTooShortASound(t *testing.T) {
	var v vad
	v.feed(tone(SampleRate / 20)) // 50ms
	if v.feed(silence(2 * SampleRate)) {
		t.Error("endpoint fired for a 50ms noise")
	}
}

// Audio arrives in whatever chunk size the client sends, and the frame
// size divides none of them. A boundary that swallowed the remainder
// would read as silence and end utterances early.
func TestVADIsIndependentOfChunkSize(t *testing.T) {
	speech := tone(SampleRate)
	var whole, split vad
	whole.feed(speech)

	for off := 0; off < len(speech); off += 999 {
		end := off + 999
		if end > len(speech) {
			end = len(speech)
		}
		split.feed(speech[off:end])
	}

	// Within one frame of each other: the split run holds a partial
	// frame back rather than measuring it short.
	if diff := whole.speechSamples - split.speechSamples; diff > vadFrame || diff < -vadFrame {
		t.Errorf("speech measured as %d in one chunk and %d in many", whole.speechSamples, split.speechSamples)
	}
}

func tone(n int) []float32 {
	s := make([]float32, n)
	for i := range s {
		s[i] = float32(0.3 * math.Sin(float64(i)*0.1))
	}
	return s
}

func silence(n int) []float32 { return make([]float32, n) }

// The address is typed by a person, so it is read the way a person
// writes one: copied out of a browser bar, out of a colleague's message,
// with or without the scheme, with or without the port.
func TestRemoteHostNormalization(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"http://box:8080", "box:8080"},
		{"https://box:9000", "box:9000"},
		{"box:8080", "box:8080"},
		{"http://box:8080/", "box:8080"},
		{"  http://box:8080  ", "box:8080"},
		// No port: whisper.cpp's server prints 8080 when started without
		// --port, so that is the one to assume rather than to refuse.
		{"box", "box:8080"},
		{"http://box", "box:8080"},
		{"192.168.1.50", "192.168.1.50:8080"},
		{"192.168.1.50:9999", "192.168.1.50:9999"},
	}
	for _, tc := range tests {
		if got := (Config{WhisperURL: tc.in}).RemoteHost(); got != tc.want {
			t.Errorf("remoteHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A remote engine needs nothing installed on this machine — that is the
// whole reason to use one. Readiness must not go looking for a binary.
func TestARemoteEngineNeedsNothingInstalledLocally(t *testing.T) {
	cfg := Config{Engine: EngineWhisper, WhisperURL: "http://another-box:8080"}
	if err := cfg.whisperReady(); err != nil {
		t.Errorf("a configured remote engine reported not ready: %v", err)
	}
	if ready, why := NewManager(cfg).Ready(); !ready {
		t.Errorf("Manager.Ready = false for a remote engine: %s", why)
	}
}

// And with no URL, nothing changes: the local path still has to find its
// engine and model.
func TestWithoutAURLTheLocalRequirementsStillApply(t *testing.T) {
	cfg := Config{Engine: EngineWhisper, WhisperBin: "/nope/whisper-server"}
	if err := cfg.whisperReady(); err == nil {
		t.Error("a missing local engine reported ready")
	}
}

// Half-typed and nonsense addresses resolve to "" rather than to
// something that merely looks like an address. "http://" in particular is
// what is left behind while editing the field, and it used to normalize
// to "http:/" — not something to hand a dialer, or to show back as the
// setting in force.
func TestRemoteHostRejectsWhatCannotNameAMachine(t *testing.T) {
	for _, in := range []string{
		"http://", "https://", "http:///", "//", "/", ":", ":8080", "http://:8080",
		"http:// ", "box /8080",
	} {
		if got := (Config{WhisperURL: in}).RemoteHost(); got != "" {
			t.Errorf("RemoteHost(%q) = %q, want \"\"", in, got)
		}
	}
}

// The real thing this is all about: a speech server that accepts the
// connection and then says nothing at all.
//
// The recognizer's own timeout is the only thing that ends such a request,
// and until it does the session's lock is held and the microphone cannot
// be switched off. Cancel is what Stop calls to end it early.
func TestCloseEndsATranscriptionAgainstASilentServer(t *testing.T) {
	reached := make(chan struct{}, 1)
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case reached <- struct{}{}:
		default:
		}
		<-block // never answers, which is the whole point
	}))
	defer srv.Close()
	defer close(block)

	rec, err := openWhisper(Config{Engine: EngineWhisper, WhisperURL: srv.URL, WhisperAPI: "whispercpp"})
	if err != nil {
		t.Fatalf("openWhisper: %v", err)
	}
	w := rec.(*whisperRecognizer)
	w.Accept(tone(SampleRate)) // a second of speech, so there is something to commit

	done := make(chan string, 1)
	go func() { done <- w.Final(context.Background()) }()

	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the request never reached the server")
	}

	// Cancel spares it: this is a sentence someone said, and Stop calls
	// Cancel on its way to waiting for exactly this transcription. Ending
	// it here is how the last sentence of every dictation went missing.
	w.Cancel()
	select {
	case text := <-done:
		t.Fatalf("Cancel ended the sentence it was supposed to wait for, returning %q", text)
	case <-time.After(200 * time.Millisecond):
	}

	// Close does end it. Once the session is gone there is nowhere to
	// deliver the text, and waiting on a server that will never answer is
	// the one thing that must not happen.
	go w.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not end a transcription that will never be answered")
	}
}

// Timestamps come out of the text whatever the server was started with.
//
// A local engine is told --no-timestamps because localcode chose its
// command line. A server on another machine was started by someone else,
// and none of the upload formats let a client turn them off — so a remote
// engine can only behave like a local one if they are removed here.
func TestCleanTranscriptRemovesTimestamps(t *testing.T) {
	tests := []struct{ in, want string }{
		{"[00:00:00.000 --> 00:00:02.000]  안녕하세요", "안녕하세요"},
		{"[00:00.000 --> 00:02.000] hello there", "hello there"},
		{"00:00:00,000 --> 00:00:02,000 hello", "hello"},
		{"<|0.00|> hello <|2.00|> there", "hello there"},
		// Not a timestamp: an ordinary parenthesis in speech stays.
		{"이 함수(비동기)를 async로", "이 함수(비동기)를 async로"},
		// Nor is a time someone said out loud.
		{"회의는 10:30에 시작합니다", "회의는 10:30에 시작합니다"},
	}
	for _, tt := range tests {
		if got := cleanTranscript(tt.in); got != tt.want {
			t.Errorf("cleanTranscript(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// A remote server started with --no-fallback drops exactly the segments
// fast speech produces, and its command line is not ours to change. The
// increment is sent per request instead, so the decode has the same
// second chance a local engine has.
func TestTheWhisperCppDialectAsksForDecodingFallback(t *testing.T) {
	api, ok := apiByName("whispercpp")
	if !ok {
		t.Fatal("the whispercpp dialect is gone")
	}
	if api.extra["temperature_inc"] != "0.2" {
		t.Errorf("temperature_inc = %q, want whisper.cpp's own default of 0.2", api.extra["temperature_inc"])
	}
}
