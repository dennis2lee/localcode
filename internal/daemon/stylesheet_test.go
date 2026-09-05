package daemon

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every custom property the stylesheet reads has to be one it defines.
//
// This is not a style rule. A var() naming a property that does not exist
// is not an error anywhere: the declaration is simply dropped and the
// element keeps whatever it inherited, so the rule quietly does nothing
// and the page still looks almost right. The stylesheet shipped for
// months with `color: var(--fg)` on the tool-call hover — a property that
// has never existed — and the only symptom was a hover that did not
// highlight, which is exactly the kind of thing nobody files.
func TestEveryCustomPropertyUsedIsDefined(t *testing.T) {
	css, err := os.ReadFile("static/style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	text := string(css)

	defined := map[string]bool{}
	for _, m := range regexp.MustCompile(`(--[A-Za-z0-9-]+)\s*:`).FindAllStringSubmatch(text, -1) {
		defined[m[1]] = true
	}

	var missing []string
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`var\(\s*(--[A-Za-z0-9-]+)`).FindAllStringSubmatch(text, -1) {
		name := m[1]
		if defined[name] || seen[name] {
			continue
		}
		seen[name] = true
		missing = append(missing, name)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("style.css reads custom properties it never defines: %s\n"+
			"a var() with no definition is dropped silently — the rule does nothing",
			strings.Join(missing, ", "))
	}
}

// The colours a viewer cannot tell apart are the ones the page never
// draws in. Declaring support for a light scheme it has no palette for
// left the scrollbars and the default form controls rendering light on a
// dark page for anyone whose system is set to light.
func TestTheStylesheetDoesNotClaimALightPaletteItLacks(t *testing.T) {
	css, err := os.ReadFile("static/style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	text := string(css)

	claimsLight := regexp.MustCompile(`color-scheme:[^;]*\blight\b`).MatchString(text)
	hasLightPalette := strings.Contains(text, "prefers-color-scheme: light")
	if claimsLight && !hasLightPalette {
		t.Error("style.css declares a light colour scheme but defines no light palette; " +
			"either add one under prefers-color-scheme: light, or say color-scheme: dark")
	}
}

// Every viewport-relative size undoes the page's own zoom.
//
// The page owns ctrl+wheel and applies the factor as a CSS zoom on the
// root element (js/zoom.js). CSS zoom deliberately does not scale
// viewport-percentage units, so a cap written as `max-height: 86vh`
// computes against the *unzoomed* viewport and stops capping the moment
// somebody zooms in: at 1.5x the Settings panel was taller than the
// window with its Close button off the bottom and nothing to scroll,
// because the box fitted its own content and its overflow-y had nothing
// to do.
//
// The container zoom this replaced shrank the viewport in CSS px instead,
// which is why the caps held before v0.100.0 and why nobody had seen it.
// So every vh and vw here is divided by --zoom, which applyZoom
// publishes, and this keeps the next one honest.
func TestEveryViewportSizeIsDividedByThePagesZoom(t *testing.T) {
	css, err := os.ReadFile(filepath.Join("static", "style.css"))
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	// A vh/vw length, and what sits immediately before it in the same
	// calc(): a bare one has no division by the factor anywhere near.
	unit := regexp.MustCompile(`[0-9.]+v[hw]\b`)
	for i, line := range strings.Split(string(css), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") {
			continue // a comment may say "86vh" while explaining this
		}
		if !unit.MatchString(line) {
			continue
		}
		if strings.Contains(line, "var(--zoom") {
			continue
		}
		t.Errorf("style.css:%d sizes something against the viewport without undoing the page's zoom; "+
			"write it as calc(<n>vh / var(--zoom, 1)):\n\t%s", i+1, trimmed)
	}
}

// A hidden element stays hidden, whatever else the sheet says about
// display.
//
// The browser's own [hidden] rule is a display:none at the very bottom of
// the cascade, so any class selector setting display beats it. Giving
// .pill a display:inline-flex un-hid every pill the page had hidden — the
// stop button appeared with nothing to stop, and the jump-to-latest
// button sat over the transcript at all times.
func TestHiddenBeatsEveryDisplayRule(t *testing.T) {
	css, err := os.ReadFile(filepath.Join("static", "style.css"))
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	if !regexp.MustCompile(`\[hidden\]\s*\{[^}]*display:\s*none\s*!important`).Match(css) {
		t.Error("style.css has no [hidden] { display: none !important } — a class rule that sets " +
			"display will silently un-hide anything the page hides")
	}
}

// The page's icon is the same artwork the window and the installer wear.
//
// A browser tab showed the blank-page glyph, because there was no icon at
// all. Now there is one, and it is a copy — this directory is what the
// daemon embeds and serves, build/icon/icon.svg is what the packagers
// rasterise — so the two have to be held together or they drift the way
// the old brick's colours drifted from the stylesheet they came from.
func TestThePagesIconIsTheApplicationIcon(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("static", "icon.svg"))
	if err != nil {
		t.Fatalf("read the page's icon: %v", err)
	}
	source, err := os.ReadFile(filepath.Join("..", "..", "build", "icon", "icon.svg"))
	if err != nil {
		t.Fatalf("read the application icon: %v", err)
	}
	// The comment above each differs on purpose; the drawing must not.
	draw := func(b []byte) string {
		s := string(b)
		if i := strings.Index(s, "<svg"); i >= 0 {
			return s[i:]
		}
		return s
	}
	if draw(page) != draw(source) {
		t.Errorf("static/icon.svg has drifted from build/icon/icon.svg; they are one mark:\n"+
			"\tthe page's:\n%s\n\tthe application's:\n%s", draw(page), draw(source))
	}
	index, err := os.ReadFile(filepath.Join("static", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), `rel="icon"`) {
		t.Error("index.html links no icon, so a browser tab shows the blank-page glyph")
	}
}

// The transcript, the composer and the status strip begin and end on the
// same two verticals.
//
// The transcript used to be capped at a reading measure and centred, and
// the composer at that measure plus six rem — so the middle column of a
// three-column window was a ribbon with empty ground either side of it,
// and a table or a diff wrapped inside it while the window had room to
// spare. All three now span and keep one gutter, which is the only thing
// holding the text off the panel rules. A rule that goes back to its own
// padding is how they drift apart again.
func TestTheTranscriptAndTheComposerShareOneGutter(t *testing.T) {
	css, err := os.ReadFile("static/style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	text := string(css)

	for _, want := range []struct{ selector, padding string }{
		{"#transcript {", "padding: 16px var(--gutter);"},
		{"footer {", "padding: 12px var(--gutter);"},
		{"#prompt-status {", "padding: 0 var(--gutter) 10px;"},
	} {
		i := strings.Index(text, want.selector)
		if i < 0 {
			t.Errorf("no %s rule in the stylesheet", want.selector)
			continue
		}
		block := text[i:]
		if j := strings.Index(block, "}"); j >= 0 {
			block = block[:j]
		}
		if !strings.Contains(block, want.padding) {
			t.Errorf("%s does not set %q — it no longer lines up with the other two", want.selector, want.padding)
		}
	}

	// And nothing puts the cap back. A max-width on the transcript's
	// children is what centred the column; the composer's was the same
	// rule spelled with a constant added to it.
	for _, gone := range []string{"--measure", "max-width: var(--gutter)"} {
		if strings.Contains(text, gone) {
			t.Errorf("style.css still carries %q — the reading measure is back and the column is a ribbon again", gone)
		}
	}
}
