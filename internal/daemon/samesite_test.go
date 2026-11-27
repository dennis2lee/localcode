package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// httptest.NewRequest sends to example.com unless told otherwise, and
// the suite's handler tests use it that way. In this test binary that is
// the machine's own name, so those tests reach the routes they test; the
// tests below name what a request was really sent to.
func init() {
	machineName = func() (string, error) { return "example.com", nil }
}

// sameSiteRequest sends method path to srv with the headers given; "Host"
// sets the host the request was sent to.
func sameSiteRequest(t *testing.T, srv *httptest.Server, method, path, body string, header map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// A page on another site cannot change anything: turning every permission
// prompt off from a form, a no-cors fetch, a sandboxed frame or a page
// on another port of this machine is refused, and the setting stays off.
// Nor can a page whose name was pointed at this machine read or change
// anything. The Web UI's own requests, and clients that are not browsers,
// go through.
func TestAnotherSitesRequestIsRefusedOnEveryRoute(t *testing.T) {
	d := newTestDaemon(t, "")
	srv := httptest.NewServer(d.Handler())
	t.Cleanup(srv.Close)
	own := strings.TrimPrefix(srv.URL, "http://")
	skip := `{"enabled":true}`

	for name, h := range map[string]map[string]string{
		"a form post from another site":       {"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"},
		"a no-cors fetch from another site":   {"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "no-cors"},
		"another origin, no Sec-Fetch-Site":   {"Origin": "https://evil.example"},
		"another site, no Origin":             {"Sec-Fetch-Site": "cross-site"},
		"another port on this machine":        {"Origin": "http://localhost:3000", "Sec-Fetch-Site": "same-site"},
		"a sandboxed frame or a file: page":   {"Origin": "null", "Sec-Fetch-Site": "cross-site"},
		"a null origin, no Sec-Fetch-Site":    {"Origin": "null"},
		"a name pointed at this machine":      {"Host": "rebind.example:4096", "Origin": "http://rebind.example:4096", "Sec-Fetch-Site": "same-origin"},
		"a name pointed at it, no browser id": {"Host": "rebind.example:4096"},
	} {
		if resp := sameSiteRequest(t, srv, "POST", "/api/permissions/skip", skip, h); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, resp.StatusCode)
		}
	}
	if d.Loop.Config.PermissionsSkipped() {
		t.Fatal("another site turned every permission prompt off")
	}

	// A rebound name reads nothing either: not the API, not a stream, not
	// the page.
	for _, path := range []string{"/api/sessions", "/api/sessions/s1/events", "/"} {
		if resp := sameSiteRequest(t, srv, "GET", path, "", map[string]string{"Host": "rebind.example", "Sec-Fetch-Site": "same-origin"}); resp.StatusCode != http.StatusForbidden {
			t.Errorf("GET %s sent to a rebound name: %d, want 403", path, resp.StatusCode)
		}
	}
	// Another site's page reads no answer without CORS, but a request it
	// makes still runs; a GET on the API is refused as well.
	if resp := sameSiteRequest(t, srv, "GET", "/api/sessions", "", map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "no-cors"}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("another site's GET on the API: %d, want 403", resp.StatusCode)
	}
	// A link on another site may open the Web UI in a tab; it may not put
	// it in a frame, or open the API.
	for _, c := range []struct {
		path, dest string
		want       int
	}{
		{"/", "document", http.StatusOK},
		{"/", "iframe", http.StatusForbidden},
		{"/api/sessions", "document", http.StatusForbidden},
	} {
		resp := sameSiteRequest(t, srv, "GET", c.path, "", map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": c.dest})
		if resp.StatusCode != c.want && !(c.want == http.StatusOK && resp.StatusCode == http.StatusNotFound) {
			t.Errorf("another site's link to %s as a %s: %d, want %d", c.path, c.dest, resp.StatusCode, c.want)
		}
	}

	// The Web UI's own request, by every name this machine answers to.
	for _, host := range []string{own, "localhost:4096", "app.localhost", "[::1]:4096", "example.com:4096", "EXAMPLE.com.local"} {
		h := map[string]string{"Host": host, "Origin": "http://" + host, "Sec-Fetch-Site": "same-origin"}
		if resp := sameSiteRequest(t, srv, "POST", "/api/permissions/skip", `{"enabled":false}`, h); resp.StatusCode != http.StatusOK {
			t.Errorf("the page's own request to %s: %d, want 200", host, resp.StatusCode)
		}
	}
	// A typed address is a navigation from nowhere.
	if resp := sameSiteRequest(t, srv, "GET", "/api/sessions", "", map[string]string{"Sec-Fetch-Site": "none"}); resp.StatusCode != http.StatusOK {
		t.Errorf("an address typed in: %d, want 200", resp.StatusCode)
	}
	// The TUI, `localcode run`, curl: no Origin, no Sec-Fetch-Site.
	if resp := sameSiteRequest(t, srv, "POST", "/api/permissions/skip", skip, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("a client that is not a browser: %d, want 200", resp.StatusCode)
	}
	if !d.Loop.Config.PermissionsSkipped() {
		t.Fatal("the client's own request did not take")
	}
}

// The name the daemon was started to listen on is its own, and only that
// one: a daemon on --listen gpu-box:4096 answers gpu-box.
func TestTheListenNameIsTheDaemonsOwn(t *testing.T) {
	d := newTestDaemon(t, "")
	d.ListenHost = "gpu-box"
	srv := httptest.NewServer(d.Handler())
	t.Cleanup(srv.Close)
	for host, want := range map[string]int{
		"gpu-box:4096":       http.StatusOK,
		"GPU-BOX.:4096":      http.StatusOK,
		"gpu-box.evil:4096":  http.StatusForbidden,
		"other-box:4096":     http.StatusForbidden,
		"localhost.evil:443": http.StatusForbidden,
	} {
		if resp := sameSiteRequest(t, srv, "GET", "/api/sessions", "", map[string]string{"Host": host}); resp.StatusCode != want {
			t.Errorf("GET sent to %s: %d, want %d", host, resp.StatusCode, want)
		}
	}
}

// The browser panel's frame stream is an <img>, and opening it says a
// window is watching, which is what offers the model the browser tool. A
// browser without Fetch Metadata (Safari before 16.4) sends neither
// Sec-Fetch-Site nor Origin on another site's <img>, so RefuseOtherSites
// alone let it through and the conversation read as watched. From the
// review of 720d215.
func TestAnotherSitesImageDoesNotWatchTheBrowser(t *testing.T) {
	d, srv, id := browserDaemon(t)
	open := func(header map[string]string) (int, bool) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/sessions/"+id+"/browser/stream", nil)
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode, d.BrowserViewing(id)
	}
	for name, h := range map[string]map[string]string{
		"another site's <img>, no Fetch Metadata": {"Referer": "http://evil.example/"},
		"a page that sends no Referer":            nil,
		"a Referer on another port":               {"Referer": "http://127.0.0.1:1/"},
	} {
		if code, watched := open(h); code != http.StatusForbidden || watched {
			t.Errorf("%s: %d, watched %v; want 403, not watched", name, code, watched)
		}
	}
	// The panel's own <img>, in a browser with Fetch Metadata and in one
	// without.
	for name, h := range map[string]map[string]string{
		"the panel, Fetch Metadata": {"Sec-Fetch-Site": "same-origin"},
		"the panel, Referer only":   {"Referer": srv.URL + "/"},
	} {
		if code, watched := open(h); code != http.StatusOK || !watched {
			t.Errorf("%s: %d, watched %v; want 200, watched", name, code, watched)
		}
	}
}

// The machine's own name, as os.Hostname gives it, in any case and with or
// without .local. From the review of 720d215: "BUILD.LOCAL" kept ".local"
// on and refused "build".
func TestTheMachinesOwnNameInAnyCase(t *testing.T) {
	was := machineName
	t.Cleanup(func() { machineName = was })
	for name, cases := range map[string]map[string]bool{
		"BUILD.LOCAL": {"build": true, "BUILD.local": true, "build.local.local": false, "builder": false},
		"Build":       {"build": true, "build.local": true, "build.lan": false},
		"build.":      {"build": true, "build.local": true},
	} {
		machineName = func() (string, error) { return name, nil }
		for host, want := range cases {
			if got := ownHost(host, ""); got != want {
				t.Errorf("os.Hostname %q: ownHost(%q) = %v, want %v", name, host, got, want)
			}
		}
	}
	machineName = func() (string, error) { return "", errors.New("no name") }
	if ownHost("build", "") {
		t.Error("a machine with no name answered to one")
	}
}
