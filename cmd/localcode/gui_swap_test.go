package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

// The window keeps one listener for its whole life, so what it serves has
// to be swappable underneath a page that is still connected: requests
// already inside the old handler finish there, and new ones go to the
// new one.
func TestTheWindowHandlerCanBeSwappedUnderTheWindow(t *testing.T) {
	answer := func(text string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, text) })
	}
	front := newSwapHandler(answer("old"))
	srv := httptest.NewServer(front)
	defer srv.Close()

	get := func() string {
		t.Helper()
		resp, err := http.Get(srv.URL + "/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if got := get(); got != "old" {
		t.Fatalf("before the swap: %q", got)
	}
	front.Store(answer("new"))
	if got := get(); got != "new" {
		t.Fatalf("after the swap: %q", got)
	}
}

// The process's alive pipe has to outlive the daemon that first used it.
//
// Held in a local it did not: a handoff exists to stop referring to the
// old daemon, so the moment the window swapped to the proxy the pipe
// became unreachable and os.File's finalizer closed it. The successor
// read EOF and exited, and the update either timed out or finished onto
// a backend that answered 502 to everything. The same fault sat in the
// two startup paths until every one of them was moved onto this accessor.
func TestTheProcessAlivePipeSurvivesCollection(t *testing.T) {
	p, err := processAlivePipe()
	if err != nil {
		t.Fatal(err)
	}
	again, err := processAlivePipe()
	if err != nil {
		t.Fatal(err)
	}
	if again != p {
		t.Fatal("a second call made a second pipe; every generation of successor watches the same end")
	}

	// Drop every local reference and make the collector look twice: a
	// finalizer runs on the cycle after the object is found unreachable.
	p, again = nil, nil
	_, _ = p, again
	runtime.GC()
	runtime.GC()

	held, err := processAlivePipe()
	if err != nil {
		t.Fatal(err)
	}
	// Writing is what proves the descriptor is still open. A collected
	// write end fails here with "file already closed", which is exactly
	// what the successor saw as EOF on the other side.
	if _, err := held.w.Write([]byte{0}); err != nil {
		t.Fatalf("the alive pipe was closed under us: %v", err)
	}
}
