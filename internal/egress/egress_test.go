package egress

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Where this process may connect.
//
// Permission rules answer "which tool, on which path" and say nothing
// about destinations. This is the half localcode can actually enforce:
// its own outbound connections. What it cannot is a shell command's own
// sockets, and the package says so rather than implying otherwise.

func TestNothingIsCheckedUntilItIsTurnedOn(t *testing.T) {
	var p Policy
	for _, host := range []string{"example.com", "10.0.0.1", "anything.at.all"} {
		if !p.Permits(host) {
			t.Errorf("%s was refused by a policy nobody turned on", host)
		}
	}
}

func TestTheAllowListIsMatchedByName(t *testing.T) {
	p := Policy{Enforced: true, Allow: []string{"api.anthropic.com", "*.internal.example"}}
	for _, c := range []struct {
		host string
		want bool
	}{
		{"api.anthropic.com", true},
		// Case and a trailing dot are the same name.
		{"API.Anthropic.COM", true},
		{"api.anthropic.com.", true},
		// A wildcard covers sub-domains and the base itself, because
		// somebody writing "*.internal.example" means that whole estate.
		{"build.internal.example", true},
		{"deep.build.internal.example", true},
		{"internal.example", true},
		// And nothing else. The suffix trap is the one worth naming: a
		// rule must not let evil-example.com through.
		{"anthropic.com", false},
		{"notapi.anthropic.com", false},
		{"internal.example.evil.com", false},
		{"example.com", false},
	} {
		if got := p.Permits(c.host); got != c.want {
			t.Errorf("Permits(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

// Loopback is always allowed, and that is not a hole: a local model
// server is the case this project is built around, the daemon talks to
// itself, and nothing leaves the machine through it.
func TestLoopbackIsAlwaysAllowed(t *testing.T) {
	p := Policy{Enforced: true, Allow: []string{"nothing.example"}}
	for _, host := range []string{"localhost", "127.0.0.1", "::1"} {
		if !p.Permits(host) {
			t.Errorf("%s was refused; a local model server would stop working", host)
		}
	}
}

// The check has to sit where every client meets, which is the transport.
func TestInstalledPolicyBlocksARealRequest(t *testing.T) {
	// A server on loopback stands in for an allowed host, and a name
	// nothing resolves for stands in for a refused one — the point being
	// that the refusal arrives before any lookup.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	// Install is once per process, so this test drives the transport
	// wrapper directly rather than through the package-level installer:
	// a second test in the same binary would otherwise silently get the
	// first one's policy.
	p := Policy{Enforced: true, Allow: []string{"api.example.com"}}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Skip("the default transport is not an *http.Transport here")
	}
	client := &http.Client{Transport: Wrap(base, p)}

	// Loopback: allowed, and the request really goes through.
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("a loopback request was refused: %v", err)
	}
	resp.Body.Close()

	// Anywhere else: refused, and the refusal says which host and where
	// to change it, because "no route to host" and "not allowed" are
	// different problems with different fixes.
	_, err = client.Get("https://blocked.example.org/x")
	if err == nil {
		t.Fatal("a request outside the allow list went through")
	}
	var blocked *BlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("the refusal is not a BlockedError: %v", err)
	}
	if blocked.Host != "blocked.example.org" {
		t.Errorf("the refusal names %q, want the host that was refused", blocked.Host)
	}
	for _, want := range []string{"not allowed", "network.egress", "Loopback"} {
		if !strings.Contains(blocked.Error(), want) {
			t.Errorf("the message does not say %q: %s", want, blocked.Error())
		}
	}
}
