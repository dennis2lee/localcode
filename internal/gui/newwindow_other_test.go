//go:build gui && !windows

package gui

import (
	"reflect"
	"strings"
	"testing"
)

// The program a link is handed to, without starting it: a test that opened
// a browser would do it on whichever machine runs the tests.
func TestALinkOpensInTheSystemsDefaultBrowser(t *testing.T) {
	const u = "https://example.com/a?b=1&c=2"
	for goos, want := range map[string][]string{
		"darwin":  {"open", u},
		"linux":   {"xdg-open", u},
		"freebsd": {"xdg-open", u},
	} {
		name, args := browserCommand(goos, u)
		if got := append([]string{name}, args...); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %q, want %q", goos, got, want)
		}
	}
}

// What the page sends is checked by the rules Windows uses before anything
// is started: only a web address with a host goes to the system.
func TestOnlyAWebAddressIsHandedToTheSystem(t *testing.T) {
	for _, raw := range []string{
		"",
		"javascript:alert(1)",
		"file:///etc/passwd",
		"https:example.com",
		"https://",
		"https://example.com/a b",
		"https://example.com/\"-a",
		"smb://host/share",
	} {
		err := openExternal(raw)
		if err == nil || !strings.HasPrefix(err.Error(), "not opened: ") {
			t.Errorf("%q: %v, want it refused before anything starts", raw, err)
		}
	}
}
