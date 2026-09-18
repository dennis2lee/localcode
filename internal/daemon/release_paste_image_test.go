package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// A pasted image reaches the model as an image, through the real route.
//
// Everything under the surface was built in two earlier changes, and the
// join between them is this handler: the browser posts base64 in the
// message body and the daemon has to turn it into the image block a turn
// carries. A test that stops at the JSON would pass while the bytes went
// nowhere, so this one reads what the model was actually sent.
func TestAPastedImageReachesTheModel(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"seen.\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer model.Close()

	d := newTestDaemon(t, model.URL)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	var sess struct {
		ID string `json:"id"`
	}
	resp, err := http.Post(srv.URL+"/api/sessions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	json.NewDecoder(resp.Body).Decode(&sess)
	resp.Body.Close()

	// The eight bytes a PNG starts with, so the payload is a real image
	// header rather than a string that happens to be base64.
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	body, _ := json.Marshal(map[string]any{
		"text":   "what is this",
		"images": []map[string]any{{"media_type": "image/png", "data": png}},
	})
	resp, err = http.Post(srv.URL+"/api/sessions/"+sess.ID+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("send message: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send with image: status %d, want 202", resp.StatusCode)
	}

	deadline := time.After(10 * time.Second)
	for {
		mu.Lock()
		n := len(bodies)
		mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the turn never reached the model")
		case <-time.After(10 * time.Millisecond):
		}
	}

	mu.Lock()
	sent := bodies[0]
	mu.Unlock()

	// base64 of the PNG header, which is what an OpenAI-compatible
	// endpoint carries inside a data: URL.
	const want = "iVBORw0KGgo"
	if !strings.Contains(sent, "image_url") {
		t.Errorf("the request carries no image block:\n%.400s", sent)
	}
	if !strings.Contains(sent, want) {
		t.Errorf("the request does not carry the pasted bytes (%s):\n%.400s", want, sent)
	}
	if !strings.Contains(sent, "what is this") {
		t.Errorf("the text that came with the image is missing:\n%.400s", sent)
	}
}

// An unsupported type is refused by the route, naming the type, rather
// than reaching a model that would refuse it less clearly.
func TestAnUnsupportedImageTypeIsRefusedByTheRoute(t *testing.T) {
	d := newTestDaemon(t, "http://unused.invalid")
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	var sess struct {
		ID string `json:"id"`
	}
	resp, _ := http.Post(srv.URL+"/api/sessions", "application/json", strings.NewReader(`{}`))
	json.NewDecoder(resp.Body).Decode(&sess)
	resp.Body.Close()

	body, _ := json.Marshal(map[string]any{
		"text":   "look",
		"images": []map[string]any{{"media_type": "image/bmp", "data": []byte{0x42, 0x4D}}},
	})
	resp, err := http.Post(srv.URL+"/api/sessions/"+sess.ID+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
	msg, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(msg), "image/bmp") {
		t.Errorf("the refusal does not name the type: %s", msg)
	}
}
