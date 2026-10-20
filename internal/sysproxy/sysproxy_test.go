package sysproxy

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// testResolver is the resolver with an environment and a system answer
// of the test's own. systemCalls counts how often the system was asked,
// which is the thing precedence is about.
func testResolver(env map[string]string, envProxy *url.URL, system Choice) (resolver, *atomic.Int32) {
	var calls atomic.Int32
	return resolver{
		getenv: func(k string) string { return env[k] },
		fromEnv: func(*http.Request) (*url.URL, error) {
			return envProxy, nil
		},
		system: newCache(func(*url.URL) Choice {
			calls.Add(1)
			return system
		}),
	}, &calls
}

// The environment wins when it names a proxy for the request's scheme,
// and the system is not even asked: a variable is somebody saying what
// they want for this one process.
func TestTheEnvironmentWinsOverTheSystem(t *testing.T) {
	envProxy := mustURL(t, "http://env.example.com:3128")
	system := Choice{Proxy: mustURL(t, "http://proxy.example.com:8080"), From: "the Windows proxy settings"}

	r, calls := testResolver(map[string]string{"HTTPS_PROXY": envProxy.String()}, envProxy, system)
	got, err := r.choose(context.Background(), mustURL(t, "https://api.github.com/repos"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Proxy != envProxy || got.From != "HTTPS_PROXY" {
		t.Errorf("choice = %+v, want the environment's proxy from HTTPS_PROXY", got)
	}
	if calls.Load() != 0 {
		t.Errorf("the system was asked %d times with HTTPS_PROXY set", calls.Load())
	}

	// The lower-case name is read too, and named as it was written.
	r, _ = testResolver(map[string]string{"https_proxy": envProxy.String()}, envProxy, system)
	if got, _ := r.choose(context.Background(), mustURL(t, "https://api.github.com/")); got.From != "https_proxy" {
		t.Errorf("from = %q, want https_proxy", got.From)
	}
}

// NO_PROXY is part of the environment's answer when the environment names
// a proxy: a host it excludes goes direct, and the system is not asked
// for a second opinion.
func TestNoProxyBesideAnEnvironmentProxyGoesDirect(t *testing.T) {
	system := Choice{Proxy: mustURL(t, "http://proxy.example.com:8080"), From: "the Windows proxy settings"}
	r, calls := testResolver(map[string]string{"HTTPS_PROXY": "http://env.example.com:3128", "NO_PROXY": "github.com"}, nil, system)
	got, err := r.choose(context.Background(), mustURL(t, "https://api.github.com/"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Proxy != nil {
		t.Errorf("proxy = %s, want direct", got.Proxy)
	}
	if !strings.Contains(got.Note, "NO_PROXY") {
		t.Errorf("note = %q, want it to name NO_PROXY", got.Note)
	}
	if calls.Load() != 0 {
		t.Errorf("the system was asked after the environment had answered")
	}
}

// A variable for the other scheme is not a decision about this one. Go
// sends an https request direct when only HTTP_PROXY is set, so the
// variable says nothing about it, and the system's answer stands.
func TestAnotherSchemesVariableLeavesTheSystemToDecide(t *testing.T) {
	system := Choice{Proxy: mustURL(t, "http://proxy.example.com:8080"), From: "the Windows proxy settings"}
	r, calls := testResolver(map[string]string{"HTTP_PROXY": "http://env.example.com:3128", "NO_PROXY": "example.com"}, nil, system)
	got, err := r.choose(context.Background(), mustURL(t, "https://api.github.com/"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Proxy == nil || got.Proxy.Host != "proxy.example.com:8080" {
		t.Errorf("choice = %+v, want the system's proxy", got)
	}
	if calls.Load() != 1 {
		t.Errorf("the system was asked %d times, want once", calls.Load())
	}
}

// Loopback goes direct whatever is set, as Go's own environment reading
// does. A proxy cannot reach this machine's loopback.
func TestLoopbackIsNeverProxied(t *testing.T) {
	system := Choice{Proxy: mustURL(t, "http://proxy.example.com:8080"), From: "the Windows proxy settings"}
	r, calls := testResolver(map[string]string{"HTTPS_PROXY": "http://env.example.com:3128"}, mustURL(t, "http://env.example.com:3128"), system)
	for _, raw := range []string{"https://localhost:8443/", "http://127.0.0.1:1/", "http://[::1]:80/", "https://LOCALHOST/"} {
		got, err := r.choose(context.Background(), mustURL(t, raw))
		if err != nil {
			t.Fatal(err)
		}
		if got.Proxy != nil {
			t.Errorf("%s went through %s", raw, got.Proxy)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("the system was asked about loopback")
	}
}

// The message names the proxy by host and port and nothing else: a proxy
// URL can carry a password, and this text ends up in bug reports.
func TestChoiceNeverPrintsCredentials(t *testing.T) {
	c := Choice{Proxy: mustURL(t, "http://alice:s3cret@proxy.example.com:8080"), From: "HTTPS_PROXY"}
	s := c.String()
	if strings.Contains(s, "alice") || strings.Contains(s, "s3cret") {
		t.Errorf("String() = %q, which carries the credentials", s)
	}
	if !strings.Contains(s, "proxy.example.com:8080") || !strings.Contains(s, "HTTPS_PROXY") {
		t.Errorf("String() = %q, want the host, the port and where it came from", s)
	}
	if got := (Choice{Note: "none set in HTTPS_PROXY"}).String(); got != "with no proxy (none set in HTTPS_PROXY)" {
		t.Errorf("direct String() = %q", got)
	}
}

// fakeScript is a proxy script's answer, and a record of what it was
// asked.
type fakeScript struct {
	answer autoAnswer
	err    error
	asked  []string
}

func (f *fakeScript) run(target string, _ settings) (autoAnswer, error) {
	f.asked = append(f.asked, target)
	return f.answer, f.err
}

// The order Internet Explorer set: a script first, and the fixed proxy
// only when no script answered. Every row is one setting a person can
// make in Internet Options, and what a request to api.github.com does
// under it.
func TestDecideFollowsTheWindowsOrder(t *testing.T) {
	target := mustURL(t, "https://api.github.com/repos/o/r/releases/latest")
	for _, tt := range []struct {
		name      string
		s         settings
		script    fakeScript
		wantProxy string // "" means direct
		wantFrom  string
		wantNote  string
		wantAsked bool
	}{
		{
			name:     "nothing set",
			wantNote: "none set in " + Sources,
		},
		{
			name:      "a fixed proxy",
			s:         settings{Proxy: "proxy.example.com:8080"},
			wantProxy: "proxy.example.com:8080",
			wantFrom:  "the Windows proxy settings",
		},
		{
			name:      "a fixed proxy per protocol",
			s:         settings{Proxy: "http=web.example.com:80;https=secure.example.com:8443"},
			wantProxy: "secure.example.com:8443",
			wantFrom:  "the Windows proxy settings",
		},
		{
			name:     "a fixed proxy with the host in its exceptions",
			s:        settings{Proxy: "proxy.example.com:8080", Bypass: "<local>;*.github.com"},
			wantNote: "bypass list",
		},
		{
			name:      "a script that names a proxy",
			s:         settings{ScriptURL: "http://wpad.example.com/proxy.pac"},
			script:    fakeScript{answer: autoAnswer{Proxy: "PROXY script.example.com:8080; DIRECT"}},
			wantProxy: "script.example.com:8080",
			wantFrom:  "the Windows proxy script",
			wantAsked: true,
		},
		{
			name:      "a script that answers DIRECT, over a fixed proxy",
			s:         settings{ScriptURL: "http://wpad.example.com/proxy.pac", Proxy: "proxy.example.com:8080"},
			script:    fakeScript{answer: autoAnswer{Direct: true}},
			wantNote:  "DIRECT",
			wantAsked: true,
		},
		{
			name:      "detection that finds nothing, falling back to the fixed proxy",
			s:         settings{AutoDetect: true, Proxy: "proxy.example.com:8080"},
			script:    fakeScript{err: errors.New("Windows automatic proxy detection found no proxy script")},
			wantProxy: "proxy.example.com:8080",
			wantFrom:  "the Windows proxy settings",
			wantAsked: true,
		},
		{
			// Windows' default: "Automatically detect settings" on and
			// nothing else, on a network with no script to find.
			name:      "detection that finds nothing, and nothing else set",
			s:         settings{AutoDetect: true},
			script:    fakeScript{err: errors.New("Windows automatic proxy detection found no proxy script")},
			wantNote:  "found no proxy script",
			wantAsked: true,
		},
		{
			name:      "a script whose answer has the host in its own exceptions",
			s:         settings{AutoDetect: true},
			script:    fakeScript{answer: autoAnswer{Proxy: "script.example.com:8080", Bypass: "api.github.com"}},
			wantNote:  "bypass list",
			wantAsked: true,
		},
		{
			name:      "a script that names only a proxy Go cannot speak",
			s:         settings{AutoDetect: true},
			script:    fakeScript{answer: autoAnswer{Proxy: "SOCKS socks.example.com:1080"}},
			wantNote:  "names no HTTP proxy",
			wantAsked: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			script := tt.script
			got := decide(target, tt.s, script.run)
			if tt.wantProxy == "" {
				if got.Proxy != nil {
					t.Fatalf("proxy = %s, want direct", got.Proxy)
				}
				if !strings.Contains(got.Note, tt.wantNote) {
					t.Errorf("note = %q, want it to say %q", got.Note, tt.wantNote)
				}
			} else {
				if got.Proxy == nil || got.Proxy.Host != tt.wantProxy {
					t.Fatalf("choice = %+v, want %s", got, tt.wantProxy)
				}
				if got.From != tt.wantFrom {
					t.Errorf("from = %q, want %q", got.From, tt.wantFrom)
				}
			}
			if asked := len(script.asked) > 0; asked != tt.wantAsked {
				t.Errorf("script asked = %v, want %v", asked, tt.wantAsked)
			}
			// The script is told the scheme and host, never the path or
			// query of the request it is deciding for.
			for _, a := range script.asked {
				if a != "https://api.github.com/" {
					t.Errorf("the script was asked about %q", a)
				}
			}
		})
	}
}

// A lookup that outlives the wait is not waited out: the request goes
// direct and says why. The lookup still finishes, and its answer is kept
// for the next request.
func TestASlowLookupIsNotWaitedOut(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	answer := Choice{Proxy: mustURL(t, "http://proxy.example.com:8080"), From: "the Windows proxy script"}
	c := newCache(func(*url.URL) Choice {
		calls.Add(1)
		<-release
		return answer
	})
	c.wait = 10 * time.Millisecond
	u := mustURL(t, "https://api.github.com/")

	got := c.get(context.Background(), u)
	if got.Proxy != nil || !strings.Contains(got.Note, "did not answer within") {
		t.Fatalf("choice = %+v, want direct with the reason", got)
	}

	close(release)
	// Long enough that only a lookup that never finishes could fail
	// this; the answer arrives as soon as the lookup returns.
	c.wait = time.Minute
	if got := c.get(context.Background(), u); got.Proxy == nil || got.Proxy.Host != "proxy.example.com:8080" {
		t.Errorf("after the lookup finished, choice = %+v, want its answer", got)
	}
	if calls.Load() != 1 {
		t.Errorf("the system was asked %d times, want once: the second request should reuse the first lookup", calls.Load())
	}
}

// Requests to one host at once share one lookup, and an answer is reused
// until it is older than the cache's lifetime.
func TestLookupsAreSharedAndExpire(t *testing.T) {
	var calls atomic.Int32
	c := newCache(func(*url.URL) Choice {
		calls.Add(1)
		return Choice{Note: "none set"}
	})
	now := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	var nowMu sync.Mutex
	c.now = func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}
	u := mustURL(t, "https://api.github.com/")

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.get(context.Background(), u)
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("eight requests at once made %d lookups, want one", calls.Load())
	}

	// Another host is another question.
	c.get(context.Background(), mustURL(t, "https://github.com/"))
	if calls.Load() != 2 {
		t.Fatalf("a second host made %d lookups in all, want two", calls.Load())
	}

	nowMu.Lock()
	now = now.Add(lookupTTL + time.Second)
	nowMu.Unlock()
	c.get(context.Background(), u)
	if calls.Load() != 3 {
		t.Errorf("an expired answer was reused: %d lookups, want three", calls.Load())
	}
}

// A request whose context ends stops waiting for the lookup.
func TestAnEndedRequestStopsWaiting(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	c := newCache(func(*url.URL) Choice {
		<-release
		return Choice{}
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := c.get(ctx, mustURL(t, "https://api.github.com/"))
	if got.Proxy != nil || !strings.Contains(got.Note, "request ended") {
		t.Errorf("choice = %+v, want direct because the request ended", got)
	}
}
