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
	// A '#', not a particular one. This asked for #58a6ff, a colour the
	// icon was redrawn out of, so the test failed on its own premise and
	// the package it lives in reported FAIL whatever else was true —
	// which is how a new test in it can pass and be invisible. What the
	// round trip needs is that there is a '#' to survive at all, and
	// logo_test.go is where the artwork itself is pinned.
	if !strings.Contains(html, "#") {
		t.Fatal("test premise broken: the document should contain a '#' to encode")
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

// The version on the splash is the shell's own, and after a startup
// handoff the shell is the copy being replaced. The window said
// v0.108.1 above a status line that said "starting localcode 0.109.0",
// and the version is what anybody looks at to see whether the update
// took. So the label has an id and a function that writes to it.
func TestTheSplashVersionCanBeCorrected(t *testing.T) {
	html := splashHTML("0.108.1")
	if !strings.Contains(html, `id="version"`) {
		t.Error("the version label has no id, so nothing can correct it")
	}
	if !strings.Contains(html, "window.lcVersion") {
		t.Error("the splash defines no lcVersion, so a handoff cannot say which version is coming up")
	}
	// And the call Go makes has to name that function.
	if got := jsCall("lcVersion", "v0.109.0"); !strings.Contains(got, "lcVersion") || !strings.Contains(got, "v0.109.0") {
		t.Errorf("jsCall built %q", got)
	}
}
