package gui

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The macOS window hides its title bar by drawing it in the page's own
// chrome colour, which means that colour lives in two files: --bg in the
// stylesheet and an NSColor in chrome_darwin.go. Drift between them is a
// visible seam across the top of the window that nobody would think to
// look for in a Go file — so it is checked here rather than noticed later.
//
// Deliberately not behind the "gui" build tag: the file it guards only
// compiles on a gui build for macOS, and a mismatch introduced anywhere
// else should still fail the ordinary test run.
func TestTitleBarColourMatchesTheStylesheet(t *testing.T) {
	css, err := os.ReadFile("../daemon/static/style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	m := regexp.MustCompile(`--bg:\s*#([0-9a-fA-F]{6})`).FindSubmatch(css)
	if m == nil {
		t.Fatal("style.css no longer defines --bg as a six-digit hex colour")
	}
	var want [3]int
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseInt(string(m[1][i*2:i*2+2]), 16, 32)
		if err != nil {
			t.Fatalf("parse --bg: %v", err)
		}
		want[i] = int(v)
	}

	src, err := os.ReadFile("chrome_darwin.go")
	if err != nil {
		t.Fatalf("read chrome_darwin.go: %v", err)
	}
	expected := fmt.Sprintf("colorWithSRGBRed:(%d/255.0) green:(%d/255.0) blue:(%d/255.0)", want[0], want[1], want[2])
	if !strings.Contains(string(src), expected) {
		t.Errorf("chrome_darwin.go does not paint the title bar in the page's --bg (#%s)\nwant it to contain: %s", m[1], expected)
	}
}
