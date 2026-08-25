package daemon

import (
	"os"
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
