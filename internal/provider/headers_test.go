package provider

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"localcode/internal/debuglog"
)

// teamHeaders is what a provider's config adds in these tests: a made-up
// gateway key, the shape of the case this exists for.
var teamHeaders = map[string]string{"X-Team-Key": "made-up-team-key-0001"}

// headerRecorder is a server that answers every path a provider asks, and
// notes the path and the X-Team-Key header of each request it is sent.
type headerRecorder struct {
	*httptest.Server
	mu   sync.Mutex
	seen []string
}

func newHeaderRecorder(t *testing.T) *headerRecorder {
	t.Helper()
	h := &headerRecorder{}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		h.mu.Lock()
		h.seen = append(h.seen, r.URL.Path+" "+r.Header.Get("X-Team-Key"))
		h.mu.Unlock()
		switch r.URL.Path {
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n")
			io.WriteString(w, "data: [DONE]\n\n")
		case "/v1/messages":
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		case "/v1/models":
			io.WriteString(w, `{"data":[{"id":"CodeA"}]}`)
		case "/version":
			io.WriteString(w, `{"version":"0.0.1"}`)
		case "/metrics":
			io.WriteString(w, "vllm:num_requests_running 1\n")
		case "/props":
			io.WriteString(w, `{"default_generation_settings":{"n_ctx":4096}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(h.Close)
	return h
}

// take is what was sent since the last take.
func (h *headerRecorder) take() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.seen
	h.seen = nil
	return out
}

// Every HTTP request a provider makes carries the headers its config adds,
// not only the chat request: a gateway that wants a second credential
// refuses the model list and the /llm-doctor probes without it too. Each
// call is checked on its own, so the one that forgot is the one named.
func TestConfiguredHeadersReachEveryRequest(t *testing.T) {
	srv := newHeaderRecorder(t)
	oa := NewOpenAICompat(srv.URL+"/v1", "made-up-bearer")
	oa.Headers = teamHeaders
	ad := NewAnthropicDirect("made-up-anthropic-key")
	ad.BaseURL = srv.URL
	ad.Headers = teamHeaders
	hi := []Message{{Role: RoleUser, Content: []Block{TextBlock("hi")}}}
	drain := func(events <-chan StreamEvent, err error) {
		if err != nil {
			t.Fatal(err)
		}
		for range events {
		}
	}
	for _, tc := range []struct {
		name  string
		call  func(ctx context.Context)
		paths int
	}{
		{"Chat", func(ctx context.Context) { drain(oa.Chat(ctx, ChatRequest{Model: "CodeA", Messages: hi})) }, 1},
		{"ModelList", func(ctx context.Context) { oa.ModelList(ctx) }, 1},
		{"RawChat", func(ctx context.Context) { oa.RawChat(ctx, []byte(`{"model":"CodeA","messages":[]}`)) }, 1},
		{"ServerFacts", func(ctx context.Context) { oa.ServerFacts(ctx, "CodeA") }, 3},
		{"ContextWindow", func(ctx context.Context) { oa.ContextWindow(ctx, "CodeB") }, 2},
		{"anthropic Chat", func(ctx context.Context) {
			drain(ad.Chat(ctx, ChatRequest{Model: "claude-sonnet-5-5", MaxTokens: 16, Messages: hi}))
		}, 1},
	} {
		srv.take()
		tc.call(t.Context())
		seen := srv.take()
		if len(seen) != tc.paths {
			t.Errorf("%s sent %d requests, want %d: %q", tc.name, len(seen), tc.paths, seen)
		}
		for _, s := range seen {
			if !strings.HasSuffix(s, " "+teamHeaders["X-Team-Key"]) {
				t.Errorf("%s: the request for %s did not carry the configured header", tc.name, strings.Fields(s)[0])
			}
		}
	}
}

// The headers localcode sets itself win over a configured header of the
// same name. Config validation refuses those names; this is the backstop
// for a provider built some other way.
func TestLocalcodesOwnHeadersWinOverConfiguredOnes(t *testing.T) {
	var auth, ctype, apiKey, version string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, ctype = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		apiKey, version = r.Header.Get("x-api-key"), r.Header.Get("anthropic-version")
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()
	clash := map[string]string{"Authorization": "Bearer other", "Content-Type": "text/plain", "X-Api-Key": "other", "Anthropic-Version": "1999-01-01"}

	oa := NewOpenAICompat(srv.URL, "made-up-bearer")
	oa.Headers = clash
	oa.RawChat(t.Context(), []byte(`{}`))
	if auth != "Bearer made-up-bearer" || ctype != "application/json" {
		t.Errorf("openai-compat sent Authorization %q and Content-Type %q, want its own", auth, ctype)
	}

	ad := NewAnthropicDirect("made-up-anthropic-key")
	ad.BaseURL = srv.URL
	ad.Headers = clash
	ad.Chat(t.Context(), ChatRequest{Model: "m", MaxTokens: 16, Messages: []Message{{Role: RoleUser, Content: []Block{TextBlock("hi")}}}})
	if apiKey != "made-up-anthropic-key" || version != anthropicAPIVersion || ctype != "application/json" {
		t.Errorf("anthropic sent x-api-key %q, anthropic-version %q and Content-Type %q, want its own", apiKey, version, ctype)
	}
}

// A redirect to another host does not get the configured headers. Go drops
// Authorization there and copies every other header of the first request,
// so without this a gateway key in a custom header went to whatever host a
// 307 named. The plain client beside it shows that it would have. A
// redirect that stays on the host keeps them, and once a chain has left
// the host they stay off, even on a hop that comes back.
func TestConfiguredHeadersDoNotFollowARedirectToAnotherHost(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	note := func(who string, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		seen[who] = r.Header.Get("X-Team-Key")
	}
	saw := func(who string) string {
		mu.Lock()
		defer mu.Unlock()
		return seen[who]
	}
	var home *httptest.Server
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		note("elsewhere"+r.URL.Path, r)
		if r.URL.Path == "/back" {
			http.Redirect(w, r, home.URL+"/v1/returned", http.StatusTemporaryRedirect)
			return
		}
		io.WriteString(w, `{"data":[]}`)
	}))
	defer elsewhere.Close()
	home = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		note("home"+r.URL.Path, r)
		switch r.URL.Path {
		case "/v1/models":
			http.Redirect(w, r, elsewhere.URL+"/v1/models", http.StatusTemporaryRedirect)
		case "/same/models":
			http.Redirect(w, r, "/v1/kept", http.StatusTemporaryRedirect)
		case "/round/models":
			http.Redirect(w, r, elsewhere.URL+"/back", http.StatusTemporaryRedirect)
		default:
			io.WriteString(w, `{"data":[]}`)
		}
	}))
	defer home.Close()

	oa := NewOpenAICompat(home.URL+"/v1", "made-up-bearer")
	oa.Headers = teamHeaders
	if _, _, _, err := oa.ModelList(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := saw("home/v1/models"); got != teamHeaders["X-Team-Key"] {
		t.Fatalf("the first request carried %q, so this test shows nothing", got)
	}
	if got := saw("elsewhere/v1/models"); got != "" {
		t.Errorf("a redirect to another host was sent the configured header: %q", got)
	}

	oa.BaseURL = home.URL + "/same"
	oa.ModelList(t.Context())
	if got := saw("home/v1/kept"); got != teamHeaders["X-Team-Key"] {
		t.Errorf("a redirect on the same host lost the configured header: %q", got)
	}

	oa.BaseURL = home.URL + "/round"
	oa.ModelList(t.Context())
	if got := saw("elsewhere/back"); got != "" {
		t.Errorf("the other host in a round trip was sent the header: %q", got)
	}
	if got := saw("home/v1/returned"); got != "" {
		t.Errorf("a hop back after leaving the host was sent the header: %q", got)
	}

	// The comparison: Go's own client, given the same request.
	req, _ := http.NewRequest(http.MethodGet, home.URL+"/v1/models", nil)
	req.Header.Set("X-Team-Key", teamHeaders["X-Team-Key"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := saw("elsewhere/v1/models"); got != teamHeaders["X-Team-Key"] {
		t.Errorf("a plain client did not forward the header (%q), so the case this guards against has changed", got)
	}
}

// The headers' values never reach a debug log, through the provider's own
// request path: the names travel on the request's context.
func TestConfiguredHeaderValuesAreRedactedInTheDebugLog(t *testing.T) {
	srv := newHeaderRecorder(t)
	sink, err := debuglog.Create(t.TempDir(), "s1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	oa := NewOpenAICompat(srv.URL+"/v1", "")
	oa.Headers = teamHeaders
	oa.ModelList(debuglog.With(context.Background(), sink))
	sink.Close()

	data, err := os.ReadFile(sink.Path())
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	if strings.Contains(log, teamHeaders["X-Team-Key"]) {
		t.Errorf("the debug log carries the header's value:\n%s", log)
	}
	if !strings.Contains(log, "X-Team-Key: [redacted]") {
		t.Errorf("the header is not in the log, redacted:\n%s", log)
	}
}

// Every request in this package is built by newRequest, so a request site
// added later carries the configured headers, their redaction and their
// redirect rule without anybody remembering to add them.
func TestEveryRequestIsBuiltByNewRequest(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var builders, outside []string
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "http" || !strings.HasPrefix(sel.Sel.Name, "NewRequest") {
					return true
				}
				where := fmt.Sprintf("%s in %s", fset.Position(sel.Pos()), fn.Name.Name)
				if fn.Name.Name == "newRequest" {
					builders = append(builders, where)
				} else {
					outside = append(outside, where)
				}
				return true
			})
		}
	}
	sort.Strings(outside)
	for _, where := range outside {
		t.Errorf("http.NewRequest at %s: build the request with newRequest, so it carries the provider's configured headers", where)
	}
	if len(builders) != 1 {
		t.Errorf("newRequest builds %d requests, want 1; the walk is not finding what it should: %v", len(builders), builders)
	}
}

// A redirect keeps the headers only to the same host and port, with the
// port a URL leaves out read as its scheme's: http://example.com:80 and
// http://example.com are one place, and http to https on the same name
// is a different port and so a different place.
func TestARedirectKeepsHeadersOnlyToTheSamePlace(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		kept     bool
	}{
		{"http://example.com:80/v1/models", "http://example.com/v2/models", true},
		{"https://example.com/v1/models", "https://EXAMPLE.com:443/v2/models", true},
		{"http://example.com/v1/models", "https://example.com/v1/models", false},
		{"https://example.com/v1/models", "https://example.com:8443/v1/models", false},
		{"https://example.com/v1/models", "https://api.example.com/v1/models", false},
	} {
		first, err := newRequest(context.Background(), http.MethodGet, tc.from, nil, teamHeaders)
		if err != nil {
			t.Fatal(err)
		}
		next, _ := http.NewRequestWithContext(first.Context(), http.MethodGet, tc.to, nil)
		next.Header = first.Header.Clone()
		withholdOffHost(next, []*http.Request{first})
		if kept := next.Header.Get("X-Team-Key") != ""; kept != tc.kept {
			t.Errorf("%s -> %s: header kept = %v, want %v", tc.from, tc.to, kept, tc.kept)
		}
	}
}

// A provider made without a client gets the one a constructor would have
// given it, on the chat stream as on every other request, rather than a
// nil client to call.
func TestAChatWithoutAClientUsesTheDefaultOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)
	p := &OpenAICompat{BaseURL: srv.URL, Headers: teamHeaders}
	ch, err := p.Chat(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: []Block{TextBlock("hi")}}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	for range ch {
	}
}

// A gateway in front of the Anthropic API that wants a header of its own
// gets the same hint an openai-compat refusal does.
func TestAnAnthropicRefusalThatNamesAHeaderIsHinted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Missing required header: X-Team-Key"}}`))
	}))
	t.Cleanup(srv.Close)
	p := NewAnthropicDirect("made-up-anthropic-key-0001")
	p.BaseURL = srv.URL
	_, err := p.Chat(context.Background(), ChatRequest{
		Model: "CodeA", MaxTokens: 16,
		Messages: []Message{{Role: RoleUser, Content: []Block{TextBlock("hi")}}},
	})
	if err == nil || !strings.Contains(err.Error(), "hint: the endpoint asks for request header X-Team-Key, which this provider does not send") {
		t.Errorf("err = %v, want the header hint", err)
	}
}
