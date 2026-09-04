package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plainAnswerServer is a model that answers with text and asks for no
// tools, so a turn is exactly one exchange.
func plainAnswerServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range textChunks("done") {
			w.Write([]byte("data: " + c + "\n\n"))
		}
		w.Write([]byte("data: [DONE]\n\n"))
		w.(http.Flusher).Flush()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func debugLogFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "localcode-debug-") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

// One file per prompt, in the workspace, holding the request that went
// out and the answer that came back. The whole point of the feature, so
// it is checked through a real turn rather than at the sink.
func TestDebugLogWritesOneFilePerPrompt(t *testing.T) {
	loop := newSmartLoop(t, plainAnswerServer(t).URL)
	ws := t.TempDir()
	if _, err := loop.Store.CreateSessionIn("s1", "", "general-purpose", ws, true); err != nil {
		t.Fatal(err)
	}

	// Off: nothing is written.
	if err := loop.SendMessage(context.Background(), "s1", "general-purpose", "first"); err != nil {
		t.Fatal(err)
	}
	if got := debugLogFiles(t, ws); len(got) != 0 {
		t.Fatalf("%d file(s) written with the switch off", len(got))
	}

	if _, err := loop.routeDebugLog("s1", "/debug-log"); err != nil {
		t.Fatal(err)
	}
	if !loop.DebugLogEnabled() {
		t.Fatal("the toggle did not turn it on")
	}
	if err := loop.SendMessage(context.Background(), "s1", "general-purpose", "second"); err != nil {
		t.Fatal(err)
	}
	if err := loop.SendMessage(context.Background(), "s1", "general-purpose", "third"); err != nil {
		t.Fatal(err)
	}
	files := debugLogFiles(t, ws)
	if len(files) != 2 {
		t.Fatalf("%d file(s) after two prompts, want one each: %v", len(files), files)
	}
	body, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{"POST ", "chat/completions", "200 OK", "# localcode debug log"} {
		if !strings.Contains(got, want) {
			t.Errorf("log lacks %q:\n%s", want, got)
		}
	}

	// The same command again turns it off, and the files already written
	// stay.
	if _, err := loop.routeDebugLog("s1", "/debug-log"); err != nil {
		t.Fatal(err)
	}
	if loop.DebugLogEnabled() {
		t.Fatal("the second call did not turn it off")
	}
	if err := loop.SendMessage(context.Background(), "s1", "general-purpose", "fourth"); err != nil {
		t.Fatal(err)
	}
	if got := debugLogFiles(t, ws); len(got) != 2 {
		t.Errorf("%d file(s) after turning it off, want the two already written", len(got))
	}
}

// The reply says where the files go and what is in them, because a
// feature that writes the whole conversation into the workspace should
// say so once, at the moment somebody turns it on.
func TestDebugLogSaysWhereAndWhat(t *testing.T) {
	loop, sid := testLoop(t, "")
	on := func() string {
		t.Helper()
		if _, err := loop.routeDebugLog(sid, "/debug-log"); err != nil {
			t.Fatal(err)
		}
		return lastReply(t, loop, sid)
	}
	got := on()
	for _, want := range []string{"debug_log: on", "One file per prompt", "whole conversation", "This run only"} {
		if !strings.Contains(got, want) {
			t.Errorf("reply lacks %q:\n%s", want, got)
		}
	}
	if got := on(); !strings.Contains(got, "debug_log: off") || !strings.Contains(got, "stay where they are") {
		t.Errorf("off reply = %q", got)
	}
}
