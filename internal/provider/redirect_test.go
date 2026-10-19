package provider

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An https endpoint that redirects to plain http does not get the
// credential sent there. Go forwards Authorization to a redirect on the
// same host whatever the scheme, so the bearer token crossed the network
// unencrypted. A redirect that keeps the scheme is still followed.
func TestARedirectToPlainHTTPIsRefused(t *testing.T) {
	var seen string
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.Write([]byte(`{"data":[]}`))
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved" {
			w.Write([]byte(`{"data":[{"id":"m"}]}`))
			return
		}
		target := plain.URL + "/v1/models"
		if r.URL.Query().Get("keep") != "" {
			target = "/moved"
		}
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer secure.Close()

	p := NewOpenAICompat(secure.URL, "secret-token")
	p.Client.Transport = secure.Client().Transport
	_, _, _, err := p.ModelList(t.Context())
	if err == nil || !strings.Contains(err.Error(), "refused a redirect from https to http") {
		t.Errorf("a redirect to plain http: %v", err)
	}
	if seen != "" {
		t.Errorf("the plain http server was sent %q", seen)
	}

	client := debugClient()
	client.Transport = secure.Client().Transport
	req, _ := http.NewRequest(http.MethodGet, secure.URL+"/start?keep=1", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("a redirect that stays on https was refused: %v", err)
	}
	resp.Body.Close()
	if resp.Request.URL.Path != "/moved" {
		t.Errorf("ended at %s, want /moved", resp.Request.URL.Path)
	}
}

// The redirect limit is Go's own: ten.
func TestRedirectLimit(t *testing.T) {
	via := make([]*http.Request, 10)
	for i := range via {
		via[i], _ = http.NewRequest(http.MethodGet, "https://a.example.com/", nil)
	}
	next, _ := http.NewRequest(http.MethodGet, "https://a.example.com/next", nil)
	if err := refuseDowngrade(next, via); err == nil {
		t.Error("an eleventh redirect was followed")
	}
	if err := refuseDowngrade(next, via[:9]); err != nil {
		t.Errorf("a tenth redirect was refused: %v", err)
	}
	plainStart, _ := http.NewRequest(http.MethodGet, "http://localhost:1234/", nil)
	plainNext, _ := http.NewRequest(http.MethodGet, "http://localhost:1234/next", nil)
	if err := refuseDowngrade(plainNext, []*http.Request{plainStart}); err != nil {
		t.Errorf("a redirect from http to http was refused: %v", err)
	}
}
