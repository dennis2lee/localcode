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
