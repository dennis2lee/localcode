package gui

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
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

// The Windows window has no frame, so the page draws its own title bar and
// the hit test hands the buttons' rectangle back to it by coordinates.
// Those coordinates are the stylesheet's, in a second copy — and if the two
// disagree the failure is not a wrong pixel: a strip that is too wide
// swallows the buttons (they stop responding), and one too narrow leaves
// part of the page acting as the caption (clicks there drag the window).
//
// Not behind the "gui" build tag, for the same reason as the test above:
// the drift can be introduced from anywhere.
func TestWindowBarGeometryMatchesTheStylesheet(t *testing.T) {
	css, err := os.ReadFile("../daemon/static/style.css")
	if err != nil {
		t.Fatalf("read style.css: %v", err)
	}
	block := regexp.MustCompile(`(?s)#window-bar \{(.*?)\}`).FindSubmatch(css)
	if block == nil {
		t.Fatal("style.css has no #window-bar rule; the page draws no title bar")
	}
	height := pxIn(t, string(block[1]), "height")

	buttons := regexp.MustCompile(`(?s)#window-bar button \{(.*?)\}`).FindSubmatch(css)
	if buttons == nil {
		t.Fatal("style.css has no #window-bar button rule")
	}
	width := pxIn(t, string(buttons[1]), "width")

	src, err := os.ReadFile("chrome_windows.go")
	if err != nil {
		t.Fatalf("read chrome_windows.go: %v", err)
	}
	for _, want := range []struct {
		decl  string
		value int
	}{
		{"windowBarHeight = %d", height},
		// Three buttons: minimise, maximise, close.
		{"windowBarWidth  = %d", width * 3},
	} {
		if decl := fmt.Sprintf(want.decl, want.value); !strings.Contains(string(src), decl) {
			t.Errorf("chrome_windows.go and style.css disagree about the title bar\nwant chrome_windows.go to declare: %s", decl)
		}
	}
}

func pxIn(t *testing.T, block, property string) int {
	t.Helper()
	m := regexp.MustCompile(property + `:\s*(\d+)px`).FindStringSubmatch(block)
	if m == nil {
		t.Fatalf("no %s in px: %s", property, block)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("parse %s: %v", property, err)
	}
	return n
}

// Every command the page sends has to be one the window knows.
//
// The two halves are a string apart: the page calls lcWindowCommand("drag")
// and the window looks that string up. A name that only one side knows is
// a control that silently does nothing — which is precisely how v0.44.0
// shipped a title bar you could not drag, and the kind of failure that
// cannot be seen from this machine at all.
func TestEveryWindowCommandThePageSendsIsKnown(t *testing.T) {
	// Every script the page ships, not main.js alone: settings.js closes
	// the window after an update too, and a command added there was one
	// this test would not have read.
	js := pageScripts(t)
	want := map[string]bool{}
	// Every call, whatever its argument looks like, and a failure for any
	// argument this cannot read. Reading only single-quoted lowercase
	// literals let "quit" in double quotes, or a hyphenated name, pass
	// unchecked into a handler that ignores what it does not know.
	// And no mention of the function that is neither a call nor the check
	// that it exists: an alias ("const f = window.lcWindowCommand") calls it
	// in a way no scan of call sites can follow.
	rest := windowCommandCall.ReplaceAll(js, nil)
	rest = windowCommandGuard.ReplaceAll(rest, nil)
	if n := strings.Count(string(rest), "lcWindowCommand"); n > 0 {
		t.Errorf("the page mentions lcWindowCommand %d time(s) as neither a call nor a check that it exists; this test cannot tell what those send", n)
	}
	for _, m := range windowCommandCall.FindAllSubmatch(js, -1) {
		arg := string(m[1])
		if lit := quotedLiteral.FindStringSubmatch(arg); lit != nil {
			want[lit[1]+lit[2]+lit[3]] = true
			continue
		}
		// The one built argument: a resize edge, from the list read below.
		if resizeByEdge.MatchString(arg) {
			continue
		}
		t.Errorf("the page calls lcWindowCommand(%s), and this test cannot tell which command that sends", arg)
	}
	// The resize commands are built from the edge list rather than written
	// out, so they are read from the same place the page builds them.
	dom, err := os.ReadFile("../daemon/static/js/dom.js")
	if err != nil {
		t.Fatalf("read dom.js: %v", err)
	}
	edges := regexp.MustCompile(`windowEdges = \[([^\]]*)\]`).FindSubmatch(dom)
	if edges == nil {
		t.Fatal("dom.js no longer lists the window's resize edges")
	}
	for _, m := range regexp.MustCompile(`'([a-z]+)'`).FindAllSubmatch(edges[1], -1) {
		want["resize:"+string(m[1])] = true
	}
	if len(want) < 5 {
		t.Fatalf("only found %d commands in the page; the scan is wrong", len(want))
	}

	src, err := os.ReadFile("chrome_windows.go")
	if err != nil {
		t.Fatalf("read chrome_windows.go: %v", err)
	}
	// The string literals the Go scanner reads, which leaves comments
	// out: a case that was removed but whose name survives in a comment is
	// not one the window handles.
	handled := goStrings(t, "chrome_windows.go", src)
	for cmd := range want {
		if !handled[cmd] {
			t.Errorf("the page sends %q and the window does not handle it", cmd)
		}
	}
}

var (
	// A call to the bound function, optional ones included, and whatever
	// its argument is.
	windowCommandCall = regexp.MustCompile(`lcWindowCommand\s*(?:\?\.)?\s*\(\s*([^)]*?)\s*\)`)
	// The page's check that the function exists before it draws buttons.
	windowCommandGuard = regexp.MustCompile(`typeof\s+window\.lcWindowCommand\b`)
	// A string literal in any of JavaScript's three quotes, with no
	// escape and no interpolation in it.
	quotedLiteral = regexp.MustCompile("^(?:'([^'\\\\]*)'|\"([^\"\\\\]*)\"|`([^`$\\\\]*)`)$")
	// The resize call the page builds from its edge list.
	resizeByEdge = regexp.MustCompile(`^'resize:'\s*\+\s*edge$`)

	blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	htmlComment  = regexp.MustCompile(`(?s)<!--.*?-->`)
	// Every script element, whatever its attributes; one with a src is
	// a file, read with the rest.
	inlineScripts = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script\s*>`)
	scriptSrc     = regexp.MustCompile(`(?i)\bsrc\s*=`)
	pageLCCall    = regexp.MustCompile(`window\.(lc[A-Z]\w*)`)
)

// stripJS is a script with its comments removed, reading strings as
// strings: a "/*" inside one, as in 'image/*', is text, and a regular
// expression over the raw source took it for a comment and removed the
// code up to the next "*/" it found. Newlines inside a comment are kept,
// so a line number still points where it did.
func stripJS(src string) string {
	var b strings.Builder
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				end = len(src) - i - 2
			} else {
				end += 2
			}
			b.WriteString(strings.Repeat("\n", strings.Count(src[i:i+2+end], "\n")))
			i += 2 + end
		case c == '\'' || c == '"' || c == '`':
			j := i + 1
			for j < len(src) && src[j] != c {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(src))
			b.WriteString(src[i:j])
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// goStrings is every string literal the Go scanner reads in a file,
// comments left out, unquoted.
func goStrings(t *testing.T, name string, src []byte) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	var sc scanner.Scanner
	sc.Init(fset.AddFile(name, fset.Base(), len(src)), src, nil, 0)
	out := map[string]bool{}
	for {
		_, tok, lit := sc.Scan()
		if tok == token.EOF {
			return out
		}
		if tok == token.STRING {
			if v, err := strconv.Unquote(lit); err == nil {
				out[v] = true
			}
		}
	}
}

// live is a page with its comments removed.
//
// Searching the raw source for a definition or a call finds one that has
// been commented out, which the browser never makes: the hook is dead,
// every Eval reaching for it finds nothing, and nothing says so. That is
// the same silence these tests exist to break, arrived at by a different
// edit. Block comments go whole, across lines, and then every line that
// starts a line comment. Here rather than beside the splash tests because
// this file is built in every lane and those are built only with the gui
// tag.
func live(html string) string {
	html = blockComment.ReplaceAllString(html, "")
	var kept []string
	for _, line := range strings.Split(html, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// pageScripts is every script the Web UI ships, joined, with comments
// dropped: a call the browser never makes is not one a test here should
// find. Every .js file under the static tree the daemon embeds, at any
// depth, and the inline scripts of every page there: a call moved into a
// subdirectory or into index.html's own script was outside a scan of the
// flat js directory.
func pageScripts(t *testing.T) []byte {
	t.Helper()
	var all []string
	scripts := 0
	err := filepath.WalkDir("../daemon/static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch filepath.Ext(path) {
		case ".js":
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			all = append(all, stripJS(string(b)))
			scripts++
		case ".html":
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range inlineScripts.FindAllStringSubmatch(htmlComment.ReplaceAllString(string(b), ""), -1) {
				if scriptSrc.MatchString(m[1]) {
					continue
				}
				all = append(all, stripJS(m[2]))
			}
		}
		return nil
	})
	if err != nil || scripts == 0 {
		t.Fatalf("no page scripts under ../daemon/static (%v); this test no longer reads what it thinks it reads", err)
	}
	return []byte(strings.Join(all, "\n"))
}

// boundNames is every name a non-test Go file of this package binds for
// the page to call, and the file that binds it. Every file, not gui.go
// alone: a Bind added beside the platform code that serves it would have
// been invisible, dead or not.
func boundNames(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	bound := map[string]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		// Parsed, not searched: a Bind in a comment is not one, and a
		// call written across lines is still one.
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Bind" {
				return true
			}
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if name, err := strconv.Unquote(lit.Value); err == nil {
					bound[name] = f
				}
			}
			return true
		})
	}
	return bound
}

// And the function the page calls is the one Go binds.
//
// The command strings are checked above; the name they travel through is
// a third string that had nothing checking it. gui.go binds
// "lcWindowCommand" and the page tests for window.lcWindowCommand before
// drawing the title-bar buttons, so a rename on either side leaves a
// window with no minimise, maximise or close on Windows, where the system
// frame is taken away, and every test green: a call to a function that
// was never bound fails silently, and the page's own check hides the
// buttons rather than erroring.
//
// It lives here, beside the command check, because the two are one
// contract between the page and this package. This file carries no build
// tag and reads the Go files as text, so it runs in every lane, and not
// only in the gui lane that compiles them. Both directions: a name Go
// binds that the page never calls is dead, and one the page calls that
// Go never binds is the silent failure above.
func TestTheFunctionsThePageCallsAreTheOnesGoBinds(t *testing.T) {
	bound := boundNames(t)
	if len(bound) == 0 {
		t.Fatal("no Bind in this package; this test no longer reads what it thinks it reads")
	}
	js := string(pageScripts(t))
	for name, file := range bound {
		if !strings.Contains(js, "window."+name+"(") {
			t.Errorf("%s binds %s and no page script calls window.%s(...)", file, name, name)
		}
	}
	// Every lc-prefixed window function the page reaches for. The
	// splash's own hooks (lcStatus, lcVersion) are defined by the splash
	// and called from Go, and are checked in splash_test.go; the page
	// scripts never touch them.
	called := pageLCCall.FindAllStringSubmatch(js, -1)
	if len(called) == 0 {
		t.Fatal("no window.lc* in the page scripts; this test no longer reads what it thinks it reads")
	}
	reported := map[string]bool{}
	for _, m := range called {
		if _, ok := bound[m[1]]; !ok && !reported[m[1]] {
			reported[m[1]] = true
			t.Errorf("the page calls window.%s and this package does not bind it", m[1])
		}
	}
}
