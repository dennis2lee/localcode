package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The window's proxy has a mux of its own in front of the daemon, and
// that mux redirects a path with a dot segment before anything is
// forwarded. Followed with the same method, DELETE .../tasks/.. became a
// DELETE of the conversation at the daemon, whose own guard saw a clean
// path.
func TestTheWindowProxyRefusesAWriteToANonCanonicalPath(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend.Close()
	front := httptest.NewServer(successorProxy(strings.TrimPrefix(backend.URL, "http://"), nil))
	defer front.Close()

	req, _ := http.NewRequest(http.MethodDelete, front.URL+"/api/sessions/s1/tasks/..", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d, want 400", resp.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 0 {
		t.Errorf("the daemon was sent %v", seen)
	}
}
