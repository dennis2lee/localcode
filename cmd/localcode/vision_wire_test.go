package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// seenRequest is one request the fake model server got.
type seenRequest struct {
	image     string // the data URI of the image a request carried
	viewImage bool   // whether the request offered view_image
}

// visionModelHome serves a fake OpenAI-compatible model that answers "Red."
// to any request carrying an image, and points a temporary HOME's config
// at it with a profile that does not set "vision". It returns what the
// server was asked, in order.
func visionModelHome(t *testing.T) func() []seenRequest {
	t.Helper()
	type seen = seenRequest
	var mu sync.Mutex
	var requests []seen
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var s seen
		for _, m := range body.Messages {
			var parts []struct {
				Type     string `json:"type"`
				ImageURL struct {
					URL string `json:"url"`
				} `json:"image_url"`
			}
			if json.Unmarshal(m.Content, &parts) == nil {
				for _, p := range parts {
					if p.Type == "image_url" {
						s.image = p.ImageURL.URL
					}
				}
			}
		}
		for _, tool := range body.Tools {
			if tool.Function.Name == "view_image" {
				s.viewImage = true
			}
		}
		mu.Lock()
		requests = append(requests, s)
		mu.Unlock()
		answer := "the answer"
		if s.image != "" {
			answer = "Red."
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", answer)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(model.Close)

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".localcode"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{
	  "providers": {"onprem": {"type": "openai-compat", "base_url": %q}},
	  "profiles": {"vlm": {"provider": "onprem", "model": "onprem-vlm-2026"}},
	  "default_profile": "vlm",
	  "agents": {"general-purpose": {"profile": "vlm"}}
	}`, model.URL+"/v1")
	if err := os.WriteFile(filepath.Join(home, ".localcode", "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(home, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(work)
	return func() []seenRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]seenRequest(nil), requests...)
	}
}

// checkProbedThenTurns: the test image first, alone, then each turn with
// no image and view_image offered.
func checkProbedThenTurns(t *testing.T, requests []seenRequest, turns int) {
	t.Helper()
	if len(requests) != 1+turns {
		t.Fatalf("%d requests, want the test image and %d turns: %+v", len(requests), turns, requests)
	}
	if !strings.HasPrefix(requests[0].image, "data:image/png;base64,") || requests[0].viewImage {
		t.Errorf("the first request is not the test image: %+v", requests[0])
	}
	for i, r := range requests[1:] {
		if r.image != "" || !r.viewImage {
			t.Errorf("turn %d: %+v, want no image and view_image offered", i+1, r)
		}
	}
}

// The daemon shows a model its profile says nothing about a test image,
// through the model's own provider, before the first turn on it, and once
// for the process. A model that names the colour is offered view_image.
//
// The hook is wired in buildDaemon and buildOneShot; every test of the
// agent loop leaves it unset, so these are the ones that fail if the
// wiring goes.
func TestTheDaemonShowsAnUnknownModelATestImageOnce(t *testing.T) {
	requests := visionModelHome(t)
	d, stop, err := buildDaemon(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("buildDaemon: %v", err)
	}
	t.Cleanup(stop)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	for _, prompt := range []string{"first", "second"} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		var out bytes.Buffer
		err := throughDaemon(ctx, runOptions{format: formatJSON, agent: "general-purpose", session: true}, srv.URL, prompt, &out)
		cancel()
		if err != nil {
			t.Fatalf("run %q: %v\n%s", prompt, err, out.String())
		}
	}

	checkProbedThenTurns(t, requests(), 2)
}

// Found in review (agy): localcode run built its own loop and left the
// hook unset, so a run offered view_image to no model but Claude.
func TestARunShowsAnUnknownModelATestImage(t *testing.T) {
	requests := visionModelHome(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out bytes.Buffer
	if err := oneShot(ctx, runOptions{format: formatJSON, agent: "general-purpose"}, "look at fig.png", &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	checkProbedThenTurns(t, requests(), 1)
}
