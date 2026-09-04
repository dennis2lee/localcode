package debuglog

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The whole point: the request as sent and the response as received, in
// a file named for the moment the prompt started.
func TestTheExchangeIsWrittenWholeToTheFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "hello") {
			t.Errorf("the server received %q; the log must not consume the body", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	}))
	defer srv.Close()

	dir := t.TempDir()
	when := time.Date(2026, 9, 4, 15, 30, 12, 482_000_000, time.UTC)
	sink, err := Create(dir, "s1", when)
	if err != nil {
		t.Fatal(err)
	}

	c := &http.Client{Transport: Transport{}}
	req, _ := http.NewRequestWithContext(With(context.Background(), sink),
		http.MethodPost, srv.URL+"/chat/completions", strings.NewReader(`{"prompt":"hello"}`))
	req.Header.Set("Authorization", "Bearer sekret-key")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	if base := filepath.Base(sink.Path()); base != "localcode-debug-20260904-153012.482.log" {
		t.Errorf("file is named %q, want the prompt's timestamp", base)
	}
	got := read(t, sink.Path())
	for _, want := range []string{
		"POST " + srv.URL + "/chat/completions",
		`{"prompt":"hello"}`,
		"200 OK",
		`"content":"hi"`,
		"Content-Type: application/json",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log lacks %q:\n%s", want, got)
		}
	}
}

// A file that can be pasted into an issue is worth nothing if it carries
// the key. Redacted by header name, and the value replaced rather than
// shortened: a prefix identifies a key too.
func TestCredentialsNeverReachTheFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "session=abcdef")
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	sink, err := Create(t.TempDir(), "s1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{Transport: Transport{}}
	req, _ := http.NewRequestWithContext(With(context.Background(), sink), http.MethodGet, srv.URL, nil)
	for k, v := range map[string]string{
		"Authorization": "Bearer sekret-key",
		"X-Api-Key":     "sk-also-secret",
		"api-key":       "azure-secret",
	} {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	sink.Close()

	got := read(t, sink.Path())
	for _, leak := range []string{"sekret-key", "sk-also-secret", "azure-secret", "abcdef"} {
		if strings.Contains(got, leak) {
			t.Errorf("the log carries %q:\n%s", leak, got)
		}
	}
	if !strings.Contains(got, "[redacted]") {
		t.Errorf("nothing was redacted:\n%s", got)
	}
}

// Without a sink on the context the transport is the transport.
func TestNothingIsWrittenWithoutASink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	defer srv.Close()
	dir := t.TempDir()

	c := &http.Client{Transport: Transport{}}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Errorf("%d file(s) written with logging off", len(files))
	}
}

// A prompt answered by a slash command makes no model call. An empty
// file for every one of those is litter that hides the real logs.
func TestAnEmptyLogIsRemoved(t *testing.T) {
	dir := t.TempDir()
	sink, err := Create(dir, "s1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sink.Path()); !os.IsNotExist(err) {
		t.Errorf("an empty log was left behind at %s", sink.Path())
	}
}

// Two prompts in one millisecond are two files, not one overwriting the
// other.
func TestTwoPromptsInOneMillisecondGetTwoFiles(t *testing.T) {
	dir := t.TempDir()
	when := time.Now()
	a, err := Create(dir, "s1", when)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Create(dir, "s2", when)
	if err != nil {
		t.Fatal(err)
	}
	if a.Path() == b.Path() {
		t.Fatalf("both prompts opened %s", a.Path())
	}
	a.Close()
	b.Close()
}

// A binary response goes into the file as it arrived. An event-stream
// frame nobody can read is still evidence that it arrived, and escaping
// it would make the file a description of the bytes rather than the
// bytes.
func TestABinaryResponseIsWrittenByteForByte(t *testing.T) {
	payload := []byte{0x00, 0x00, 0x00, 0x1b, 0xff, 0xfe, '\n', 0x7f, 'o', 'k', 0x00}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		w.Write(payload)
	}))
	defer srv.Close()

	sink, err := Create(t.TempDir(), "s1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{Transport: Transport{}}
	req, _ := http.NewRequestWithContext(With(context.Background(), sink), http.MethodGet, srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	sink.Close()

	if !strings.Contains(read(t, sink.Path()), string(payload)) {
		t.Errorf("the binary body is not in the log byte for byte:\n%q", read(t, sink.Path()))
	}
}

// A streamed request body is left alone. Reading it to log it would
// consume what the caller was about to send.
func TestAStreamedRequestBodyIsNotedRatherThanConsumed(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	sink, err := Create(t.TempDir(), "s1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{Transport: Transport{}}
	// A reader with no Len makes ContentLength -1, which is what a
	// streamed upload looks like.
	req, _ := http.NewRequestWithContext(With(context.Background(), sink),
		http.MethodPost, srv.URL, struct{ io.Reader }{strings.NewReader("streamed payload")})
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	sink.Close()

	if string(got) != "streamed payload" {
		t.Errorf("the server received %q; the log consumed the body", got)
	}
	if !strings.Contains(read(t, sink.Path()), "streamed") {
		t.Errorf("the log does not say the body was streamed:\n%s", read(t, sink.Path()))
	}
}

// A body whose length is not known ahead of time is still logged when
// the client can produce a second copy. The AWS SDK sets GetBody on
// every signed request, which is why a Bedrock turn logged its answer
// and not its question.
func TestAnUnknownLengthBodyIsLoggedThroughGetBody(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	sink, err := Create(t.TempDir(), "s1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{Transport: Transport{}}
	req, _ := http.NewRequestWithContext(With(context.Background(), sink),
		http.MethodPost, srv.URL, struct{ io.Reader }{strings.NewReader(`{"anthropic_version":"x"}`)})
	// What a signing client does: no length up front, a fresh copy on
	// demand for the retry it might have to make.
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(`{"anthropic_version":"x"}`)), nil
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	sink.Close()

	if string(got) != `{"anthropic_version":"x"}` {
		t.Errorf("the server received %q; reading for the log disturbed the request", got)
	}
	if !strings.Contains(read(t, sink.Path()), `"anthropic_version":"x"`) {
		t.Errorf("the request body is not in the log:\n%s", read(t, sink.Path()))
	}
}
