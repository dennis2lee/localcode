package tui

import (
	"strings"
	"testing"
)

// stripTestANSI removes the escape sequences the renderer adds, so these
// tests assert on what the reader was told rather than on the styling.
func stripTestANSI(s string) string {
	return mdANSIRe.ReplaceAllString(s, "")
}

// A document exercising every construct has to come out with each one
// visibly rendered and none of the source markers left over.
func TestModelMarkdownRendersADocumentExercisingEveryConstruct(t *testing.T) {
	doc := "# Heading\n\n" +
		"Some **bold** and *italic* with `inline` code.\n\n" +
		"- one\n" +
		"  - nested\n" +
		"1. first\n" +
		"2. second\n\n" +
		"```go\nfunc main() {}\n```\n\n" +
		"```\nplain block\n```\n\n" +
		"Use ``code with `backticks` inside`` here.\n\n" +
		"See [the docs](https://example.com/a) for more.\n\n" +
		"> quoted\n\n" +
		"---\n"
	got := renderModelMarkdown(doc, 80)
	plain := stripTestANSI(got)

	for _, want := range []string{
		"Heading",
		"• one",
		"nested",
		"1. first",
		"func main() {}",
		"plain block",
		"code with `backticks` inside",
		"quoted",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("rendered document has no %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "# Heading") {
		t.Errorf("heading marker leaked into the output:\n%s", plain)
	}
	if strings.Contains(plain, "```") {
		t.Errorf("fence markers leaked into the output:\n%s", plain)
	}
	if strings.Contains(plain, "[the docs](https://example.com/a)") {
		t.Errorf("link stayed literal instead of rendering:\n%s", plain)
	}
	if !strings.Contains(plain, strings.Repeat("─", 10)) {
		t.Errorf("rule did not render as a line:\n%s", plain)
	}
	if !strings.Contains(plain, "the docs (https://example.com/a)") {
		t.Errorf("link did not render as text plus destination:\n%s", plain)
	}
	// Nested means indented further than its parent.
	oneAt := strings.Index(plain, "• one")
	nestedAt := strings.Index(plain, "nested")
	if oneAt < 0 || nestedAt < 0 || nestedAt < oneAt {
		t.Fatalf("list order wrong:\n%s", plain)
	}
	indentOf := func(idx int) int {
		start := strings.LastIndex(plain[:idx], "\n") + 1
		line := plain[start:]
		if end := strings.Index(line, "\n"); end >= 0 {
			line = line[:end]
		}
		return len(line) - len(strings.TrimLeft(line, " "))
	}
	if indentOf(nestedAt) <= indentOf(oneAt) {
		t.Errorf("nested item is not indented past its parent:\n%s", plain)
	}
	// The render is styled, not plain text.
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("no styling reached the terminal render:\n%q", got)
	}
}

// Text that is not markdown must come through word for word: identifiers
// with underscores, shell variables, prices, and pipes are prose, and a
// renderer that decorates them is corrupting the reply.
func TestTextThatIsNotMarkdownComesThroughUnharmed(t *testing.T) {
	doc := "call read_file then write_file now\n" +
		"the cost is $5 and $PATH is set\n" +
		"a|b is alternation, not a table\n" +
		"just a plain sentence here"
	got := stripTestANSI(renderModelMarkdown(doc, 80))
	// Every word survives in order, with nothing added and nothing
	// decorated: identifiers keep their underscores, dollars stay
	// dollars, and no markdown marker appears from nowhere.
	if strings.Join(strings.Fields(stripTestANSI(got)), " ") != strings.Join(strings.Fields(doc), " ") {
		t.Errorf("plain text was changed:\n got: %q\nwant words: %q", got, doc)
	}
	for _, probe := range []string{"read_file", "write_file", "$5", "$PATH", "a|b"} {
		if !strings.Contains(got, probe) {
			t.Errorf("plain text lost %q:\n%s", probe, got)
		}
	}
	if strings.Contains(renderModelMarkdown(doc, 80), "**") {
		t.Errorf("styling markers leaked into plain text")
	}
}

// Styling is paint on the screen, not content: the entry the transcript
// keeps — the same bytes a log file receives — must carry no escape
// sequences after a render.
func TestStylingNeverReachesTheStoredTranscript(t *testing.T) {
	m := newTestModel()
	m.appendModelDelta("# Title\n\nSome **bold** text with `code`.\n")
	m.endModelStream("# Title\n\nSome **bold** text with `code`.\n")
	_ = renderTranscript(m.transcript, 80)
	for _, e := range m.transcript {
		if strings.Contains(e.text, "\x1b") {
			t.Errorf("escape sequence stored in the transcript entry: %q", e.text)
		}
	}
	if len(m.transcript) != 1 || m.transcript[0].text != "# Title\n\nSome **bold** text with `code`.\n" {
		t.Errorf("the stored entry is not the raw reply: %q", m.transcript[0].text)
	}
	if out := stripTestANSI(renderTranscript(m.transcript, 80)); !strings.Contains(out, "Title") {
		t.Errorf("the styled render lost the content: %q", out)
	}
}

// A narrow window degrades the layout, never the content and never with
// a panic: prose wraps to the width while every word survives.
func TestAModelReplyDegradesOnANarrowWindow(t *testing.T) {
	doc := "a long paragraph of prose that must wrap somewhere honest\n\n- a list item with plenty of words in it\n"
	for _, width := range []int{1, 0, -5, 20, 30} {
		got := stripTestANSI(renderModelMarkdown(doc, width))
		for _, word := range []string{"paragraph", "prose", "list", "plenty"} {
			if !strings.Contains(got, word) {
				t.Errorf("width %d lost %q:\n%s", width, word, got)
			}
		}
		for _, ln := range strings.Split(got, "\n") {
			if len(ln) > 30 {
				t.Errorf("width %d left a %d-column prose line: %q", width, len(ln), ln)
			}
		}
	}
}

// Highlighting tints the control flow, strings, comments and numbers,
// and whatever it does not know passes through untouched.
func TestHighlightedCodeTintsKeywordsStringsCommentsAndNumbers(t *testing.T) {
	code := "func main() { // greet\n\ts := \"hi\"\n\tn := 42\n}"
	got := highlightCode(code, "go")
	if stripTestANSI(got) != code {
		t.Errorf("highlighting changed the code bytes:\n got: %q\nwant: %q", stripTestANSI(got), code)
	}
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("no tinting was applied:\n%q", got)
	}
	// An unknown language still colours strings and comments, and a
	// non-keyword is never tinted as one.
	plain := highlightCode("x := 1 // uno", "klingon")
	if stripTestANSI(plain) != "x := 1 // uno" {
		t.Errorf("unknown language changed the code: %q", stripTestANSI(plain))
	}
	if strings.Contains(highlightCode("format := 1", "go"), mdKeywordStyle.Render("format")) {
		t.Error("a word merely containing a keyword was tinted as one")
	}
}

// A reply still streaming has no closing fence; it renders as code
// anyway rather than leaving literal backticks on screen.
func TestAnUnterminatedFenceStillRendersAsCode(t *testing.T) {
	got := stripTestANSI(renderModelMarkdown("```go\nfunc main() {", 80))
	if strings.Contains(got, "```") {
		t.Errorf("fence marker leaked into the output: %q", got)
	}
	if !strings.Contains(got, "func main() {") {
		t.Errorf("the block content is missing: %q", got)
	}
}

// LaTeX meant as a symbol unwraps; dollars that are dollars and real
// formulae stay exactly as written.
func TestMathUnwrapsSymbolsAndLeavesTheRestAlone(t *testing.T) {
	if got := stripTestANSI(renderModelMarkdown(`a $\rightarrow$ b`, 80)); got != "a → b" {
		t.Errorf("symbol not unwrapped: %q", got)
	}
	for _, s := range []string{"it costs $5", "echo $PATH"} {
		if got := stripTestANSI(renderModelMarkdown(s, 80)); got != s {
			t.Errorf("ordinary dollars touched: %q", got)
		}
	}
}
