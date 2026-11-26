package daemon

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The Web UI's permission question keeps the line breaks of what it asks
// about, and wraps a long unbroken run inside its box. A prompt of two
// paragraphs drawn as one run of text, or one that pushes the buttons
// sideways, is a question read differently from how it was written.
func TestThePermissionQuestionKeepsItsLineBreaksAndWraps(t *testing.T) {
	css, err := os.ReadFile(filepath.Join("static", "style.css"))
	if err != nil {
		t.Fatalf("read the stylesheet: %v", err)
	}
	rule := regexp.MustCompile(`(?s)#permission-text\s*\{([^}]*)\}`).FindSubmatch(css)
	if rule == nil {
		t.Fatal("no #permission-text rule, so the question collapses its line breaks")
	}
	for _, want := range []string{`white-space:\s*pre-wrap`, `overflow-wrap:\s*anywhere`} {
		if !regexp.MustCompile(want).Match(rule[1]) {
			t.Errorf("#permission-text does not set %s: {%s}", want, rule[1])
		}
	}
}
