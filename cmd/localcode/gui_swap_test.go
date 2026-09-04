package main

import (
	"io"
	"net/http"
	"net/http/httptest"
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
