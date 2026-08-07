//go:build gui

package gui

import (
	"net/url"
	"strings"
	"testing"
)

// The status messages carry file paths and error text from arbitrary
// tools and config files. A raw quote, backslash or newline in one of
// those would end the JavaScript string early and turn the update into a
// syntax error — and because Eval reports nothing back, the result is a
// splash screen frozen on whatever it last said, with no clue why.
func TestJSCallQuotesItsArgument(t *testing.T) {
	for _, arg := range []string{
		`connecting to MCP server "weird"`,
		`C:\Users\you\.localcode\config.json`,
		"line one\nline two",
		`</script><script>alert(1)</script>`,
	} {
		got := jsCall("lcStatus", arg)
		if strings.Contains(got, "\n") {
			t.Errorf("a newline survived into the call for %q: %s", arg, got)
		}
		// The argument has to appear exactly once, as a quoted literal:
		// a bare copy of it would mean the quoting did not happen.
		if strings.Contains(got, arg) && strings.ContainsAny(arg, "\"\\\n") {
			t.Errorf("argument %q reached the call unescaped: %s", arg, got)
		}
	}
}

// The splash is shown by navigating to a data: URL, so every byte of it
// has to survive being a URL. An unencoded '#' would truncate the
// document at that point, and the icon's own markup is full of them.
func TestSplashDataURLRoundTrips(t *testing.T) {
	html := splashHTML("0.32.3")
	if !strings.Contains(html, "#58a6ff") {
		t.Fatal("test premise broken: the logo's colours should contain a '#'")
	}

	raw := dataURL(html)
	const prefix = "data:text/html;charset=utf-8,"
	if !strings.HasPrefix(raw, prefix) {
		t.Fatalf("missing data URL prefix: %.40s", raw)
	}
	decoded, err := url.PathUnescape(strings.TrimPrefix(raw, prefix))
	if err != nil {
		t.Fatalf("the encoded document is not valid percent-encoding: %v", err)
	}
	if decoded != html {
		t.Error("the document did not survive the round trip through a data URL")
	}
}

// Two things have to be in the page for it to do its job: the artwork,
// so the window is recognisably what was clicked, and the hooks Go calls
// to report progress into it.
func TestSplashCarriesTheLogoAndTheProgressHooks(t *testing.T) {
	html := splashHTML("0.32.3")
	for _, want := range []string{"<svg", "lcStatus", "lcFailed", "v0.32.3"} {
		if !strings.Contains(html, want) {
			t.Errorf("splash is missing %q", want)
		}
	}
	// An unstamped build should show no version rather than the literal
	// string "vdev", which reads as a version and is not one.
	if strings.Contains(splashHTML("dev"), "vdev") {
		t.Error(`an unstamped build shows "vdev"`)
	}
}
