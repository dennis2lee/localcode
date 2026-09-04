package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The window's proxy hands everything to the successor except the two
// routes that open native dialogs, and it corrects the one answer that
// says whether those exist.
func TestTheWindowProxyPassesThroughAndCorrectsAbilities(t *testing.T) {
	var sawPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		switch r.URL.Path {
		case "/api/workspace":
			// A daemon with no window says it cannot browse or reveal.
			json.NewEncoder(w).Encode(map[string]any{"path": "/work", "can_browse": false, "can_reveal": false})
		default:
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte(`{"via":"backend"}`))
		}
	}))
	defer backend.Close()

	front := httptest.NewServer(successorProxy(strings.TrimPrefix(backend.URL, "http://")))
	defer front.Close()

	resp, err := http.Get(front.URL + "/api/workspace")
	if err != nil {
		t.Fatal(err)
	}
	var ws map[string]any
	json.NewDecoder(resp.Body).Decode(&ws)
	resp.Body.Close()
	if ws["path"] != "/work" {
		t.Errorf("the workspace path did not come from the daemon: %v", ws)
	}
	if ws["can_browse"] != true || ws["can_reveal"] != true {
		t.Errorf("the window did not claim the abilities it has: %v", ws)
	}

	resp, err = http.Post(front.URL+"/api/sessions/S1/messages", "application/json", strings.NewReader(`{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted || sawPath != "/api/sessions/S1/messages" {
		t.Errorf("a session request did not reach the daemon: %d, last path %q", resp.StatusCode, sawPath)
	}
}

// The event stream never ends and arrives a line at a time. A proxy that
// buffered it would show a reply only when the turn finished, which is
// the one thing a stream exists not to do.
func TestTheWindowProxyStreamsWithoutBuffering(t *testing.T) {
	release := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: first\n\n"))
		w.(http.Flusher).Flush()
		<-release
		w.Write([]byte("data: second\n\n"))
	}))
	defer backend.Close()
	defer close(release)

	front := httptest.NewServer(successorProxy(strings.TrimPrefix(backend.URL, "http://")))
	defer front.Close()

	resp, err := http.Get(front.URL + "/api/sessions/S1/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	lineCh := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(resp.Body).ReadString('\n')
		lineCh <- line
	}()
	select {
	case line := <-lineCh:
		if !strings.Contains(line, "first") {
			t.Errorf("first line = %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the first event did not arrive until the stream ended: the proxy is buffering")
	}
}

// A backend that is gone used to answer 502 with an empty body, which
// reached the transcript as "POST /api/sessions/…/agent: 502" — no
// process named, nothing to look at. That is what a successor dying
// mid-session looked like from the window.
func TestADeadSuccessorSaysWhatIsWrong(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := strings.TrimPrefix(backend.URL, "http://")
	backend.Close() // the successor is gone; the window still points at it

	front := httptest.NewServer(successorProxy(addr))
	defer front.Close()

	resp, err := http.Get(front.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %s, want 502", resp.Status)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("the 502 had no readable body: %v", err)
	}
	for _, want := range []string{"not answering", addr, "handoff.log", "Reopen the window"} {
		if !strings.Contains(body.Error, want) {
			t.Errorf("the message lacks %q: %s", want, body.Error)
		}
	}
}
