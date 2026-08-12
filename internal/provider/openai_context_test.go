package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// serveJSON answers one path with one body and 404s everything else, so a
// test can prove which endpoint the probe actually used.
func serveJSON(t *testing.T, path, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Every server names this differently and none of it is in the OpenAI
// spec, so the field list is the whole feature. A shape that stops being
// recognised costs a wrong window, silently.
func TestContextWindowReadsEachServersShape(t *testing.T) {
	cases := []struct {
		name string
		path string
		body string
		want int
	}{
		{
			name: "vLLM",
			path: "/v1/models",
			body: `{"data":[{"id":"muse-glimmer-30b","object":"model","max_model_len":32768}]}`,
			want: 32768,
		},
		{
			name: "LM Studio",
			path: "/v1/models",
			body: `{"data":[{"id":"muse-glimmer-30b","max_context_length":131072,"loaded_context_length":8192}]}`,
			// The loaded size, not the model's maximum: what a request has
			// to fit inside is what the server was started with.
			want: 8192,
		},
		{
			name: "TGI and proxies",
			path: "/v1/models",
			body: `{"data":[{"id":"muse-glimmer-30b","context_length":16384}]}`,
			want: 16384,
		},
		{
			name: "llama.cpp /props",
			path: "/props",
			body: `{"default_generation_settings":{"n_ctx":4096}}`,
			want: 4096,
		},
		{
			name: "figure sent as a string",
			path: "/v1/models",
			body: `{"data":[{"id":"muse-glimmer-30b","max_model_len":"32768"}]}`,
			want: 32768,
		},
		{
			name: "nested under meta",
			path: "/v1/models",
			body: `{"data":[{"id":"muse-glimmer-30b","meta":{"n_ctx":65536}}]}`,
			want: 65536,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := serveJSON(t, tc.path, tc.body)
			p := NewOpenAICompat(srv.URL+"/v1", "")
			got, ok := p.ContextWindow(context.Background(), "muse-glimmer-30b")
			if !ok {
				t.Fatal("the server said, and the probe did not hear it")
			}
			if got != tc.want {
				t.Errorf("window = %d, want %d", got, tc.want)
			}
		})
	}
}

// A served id carries the vendor prefix and the file's own capitalisation;
// config carries whatever the user typed. Requiring them to match exactly
// would miss the answer on most real setups.
func TestContextWindowMatchesLooselyNamedModels(t *testing.T) {
	srv := serveJSON(t, "/v1/models", `{"data":[
		{"id":"Qwen/Qwen3-30B-A3B","max_model_len":40960},
		{"id":"Muse_Glimmer.30B","max_model_len":32768}
	]}`)
	p := NewOpenAICompat(srv.URL+"/v1", "")

	got, ok := p.ContextWindow(context.Background(), "muse-glimmer-30b")
	if !ok || got != 32768 {
		t.Errorf("window = %d (found=%v), want 32768 — the name differs only in punctuation", got, ok)
	}
}

// One model loaded, addressed by an alias that matches nothing: it is
// still the only thing this server can be serving.
func TestContextWindowUsesTheOnlyLoadedModel(t *testing.T) {
	srv := serveJSON(t, "/v1/models", `{"data":[{"id":"local-model","max_model_len":8192}]}`)
	p := NewOpenAICompat(srv.URL+"/v1", "")

	got, ok := p.ContextWindow(context.Background(), "whatever-i-called-it")
	if !ok || got != 8192 {
		t.Errorf("window = %d (found=%v), want 8192", got, ok)
	}
}

// Several models loaded and none matches: guessing which one would be
// worse than not answering, since the caller's fallback at least does not
// claim to know.
func TestContextWindowDoesNotGuessAmongSeveralModels(t *testing.T) {
	srv := serveJSON(t, "/v1/models", `{"data":[
		{"id":"model-a","max_model_len":8192},
		{"id":"model-b","max_model_len":131072}
	]}`)
	p := NewOpenAICompat(srv.URL+"/v1", "")

	if got, ok := p.ContextWindow(context.Background(), "model-c"); ok {
		t.Errorf("picked %d out of two models that were not asked about", got)
	}
}

// Everything about this is best-effort: a server with no such endpoint, an
// error, or a shape nobody anticipated has to leave the caller to its own
// fallback rather than producing a number.
func TestContextWindowIsSilentWhenTheServerDoesNotSay(t *testing.T) {
	cases := map[string]string{
		"no window field": `{"data":[{"id":"m","object":"model"}]}`,
		"empty list":      `{"data":[]}`,
		"not json":        `<html>404</html>`,
		"implausible":     `{"data":[{"id":"m","max_model_len":999999999999}]}`,
		"zero":            `{"data":[{"id":"m","max_model_len":0}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			srv := serveJSON(t, "/v1/models", body)
			p := NewOpenAICompat(srv.URL+"/v1", "")
			if got, ok := p.ContextWindow(context.Background(), "m"); ok {
				t.Errorf("reported %d from a server that did not say", got)
			}
		})
	}

	// And a server that is not there at all.
	p := NewOpenAICompat("http://127.0.0.1:1/v1", "")
	if _, ok := p.ContextWindow(context.Background(), "m"); ok {
		t.Error("reported a window from a server that could not be reached")
	}
}

// A probe against an authenticated endpoint has to carry the key, or it
// reads 401 as "this server has no answer" on every proxy that needs one.
func TestContextWindowSendsTheAPIKey(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"data":[{"id":"m","max_model_len":16384}]}`)
	}))
	defer srv.Close()

	p := NewOpenAICompat(srv.URL+"/v1", "sk-test")
	if _, ok := p.ContextWindow(context.Background(), "m"); !ok {
		t.Fatal("no answer")
	}
	if seen != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want the configured key", seen)
	}
}
