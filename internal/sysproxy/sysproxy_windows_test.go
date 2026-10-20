//go:build windows

package sysproxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The calls themselves, on a real Windows machine. Every other test in
// this package hands decide a script of its own; these are the only ones
// that cross into winhttp.dll, so a structure laid out wrongly for it, or
// a buffer freed twice, shows up here and nowhere else.

// Reading Internet Options returns, whatever the runner has set.
func TestTheWindowsSettingsCanBeRead(t *testing.T) {
	s, err := readSettings()
	if err != nil {
		t.Fatalf("readSettings: %v", err)
	}
	t.Logf("this machine's settings: %+v", s)
	// And the whole path, settings to choice, comes back with something
	// to say: a proxy and where it came from, or why there is none.
	c := systemChoice(mustURL(t, "https://api.github.com/"))
	if c.Proxy == nil && c.Note == "" {
		t.Errorf("a direct choice says nothing about why: %+v", c)
	}
	t.Logf("choice: %s", c)
}

// A proxy script is evaluated by Windows, not by this program: WinHTTP
// fetches it from the address in the settings, runs FindProxyForURL, and
// hands back the answer. Served from loopback, so the test needs no
// network and names no real proxy.
func TestWindowsRunsTheProxyScript(t *testing.T) {
	const script = `function FindProxyForURL(url, host) {
	if (host == "api.github.com") return "PROXY proxy.example.com:8080; DIRECT";
	return "DIRECT";
}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ns-proxy-autoconfig")
		fmt.Fprint(w, script)
	}))
	t.Cleanup(srv.Close)
	s := settings{ScriptURL: srv.URL + "/proxy.pac"}

	a, err := within(t, func() (autoAnswer, error) { return autoProxy("https://api.github.com/", s) })
	if err != nil {
		t.Fatalf("autoProxy: %v", err)
	}
	t.Logf("WinHTTP's answer for api.github.com: %+v", a)
	got := decide(mustURL(t, "https://api.github.com/repos/o/r/releases/latest"), s, autoProxy)
	if got.Proxy == nil || got.Proxy.Host != "proxy.example.com:8080" || got.From != "the Windows proxy script" {
		t.Errorf("api.github.com: choice = %+v, want proxy.example.com:8080 from the script", got)
	}

	// The script's DIRECT, for every other host.
	got = decide(mustURL(t, "https://github.com/o/r/releases/latest"), s, autoProxy)
	if got.Proxy != nil {
		t.Errorf("github.com: choice = %+v, want direct as the script says", got)
	}
}

// Detection on a network with no script to find is Windows' default, and
// what every machine without a proxy asks. It comes back, with an answer
// or with a reason, rather than holding a request.
func TestAutomaticDetectionReturns(t *testing.T) {
	a, err := within(t, func() (autoAnswer, error) {
		return autoProxy("https://api.github.com/", settings{AutoDetect: true})
	})
	t.Logf("answer %+v, err %v", a, err)
	if err != nil && err.Error() == "" {
		t.Error("a failed detection has no reason to give")
	}
}

// within runs a lookup and fails the test if it outlives every timeout
// WinHTTP was given, instead of leaving it to the package's -timeout.
func within(t *testing.T, f func() (autoAnswer, error)) (autoAnswer, error) {
	t.Helper()
	type result struct {
		a   autoAnswer
		err error
	}
	done := make(chan result, 1)
	go func() {
		a, err := f()
		done <- result{a, err}
	}()
	select {
	case r := <-done:
		return r.a, r.err
	case <-time.After(4*scriptTimeout + time.Minute):
		t.Fatal("WinHTTP did not return from a proxy lookup")
		return autoAnswer{}, nil
	}
}
