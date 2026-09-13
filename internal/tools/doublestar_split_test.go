package tools

import (
	"path/filepath"
	"testing"
)

// The "**" split, checked against a Windows-shaped pattern from a machine
// that is not Windows.
//
// This is the test that would have caught the bug, and the reason it is
// written as a string function rather than as a glob against real files:
// splitDoubleStar's whole job is separators, and the defect it exists to
// prevent is invisible on any platform where "/" already is the separator.
// Every "**" glob on Windows answered "no files match" — the pattern the
// tool advertises to the model first — and it survived three platforms and
// every existing test because the only thing in the chain with an opinion
// about separators was filepath.Match at the very end.
//
// So the input here is the literal string the Windows chain produces, typed
// out rather than built with filepath.Join, which would produce forward
// slashes on this machine and test nothing.
func TestTheDoubleStarSplitIsInSlashTermsWhateverThePlatformSpells(t *testing.T) {
	for _, c := range []struct {
		name    string
		sep     rune
		pattern string
		root    string
		suffix  string
	}{
		// What resolve produces on Windows: the model wrote
		// "**/localcode/*.go", filepath.Join turned every slash into a
		// backslash, and the split used to keep them.
		{
			name:    "windows-shaped, leading **",
			sep:     '\\',
			pattern: `C:\ws\**\localcode\*.go`,
			root:    `C:\ws`,
			suffix:  "localcode/*.go",
		},
		{
			name:    "windows-shaped, ** in the middle",
			sep:     '\\',
			pattern: `C:\ws\src\**\*.go`,
			root:    `C:\ws\src`,
			suffix:  "*.go",
		},
		// And the Unix shape, unchanged, because the fix must be an
		// identity on the platforms that were already working.
		{
			name:    "unix-shaped, leading **",
			sep:     '/',
			pattern: "/ws/**/localcode/*.go",
			root:    "/ws",
			suffix:  "localcode/*.go",
		},
		{
			name:    "unix-shaped, ** in the middle",
			sep:     '/',
			pattern: "/ws/src/**/*.go",
			root:    "/ws/src",
			suffix:  "*.go",
		},
		// A bare pattern with nothing before the "**" walks from here.
		{
			name:    "nothing before the stars",
			sep:     '/',
			pattern: "**/*.go",
			root:    ".",
			suffix:  "*.go",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, suffix := splitDoubleStarSep(c.pattern, c.sep)
			if suffix != c.suffix {
				t.Errorf("suffix = %q, want %q — a suffix carrying separators the matcher does not use matches nothing, silently", suffix, c.suffix)
			}
			if root != c.root {
				t.Errorf("root = %q, want %q", root, c.root)
			}
		})
	}
}

// The suffix the split produces is matched in slash terms, on every
// platform.
//
// filepath.Match would not do here even once the split is fixed: on Windows
// its separator is "\" and "/" is an ordinary character, so "*" crosses a
// directory boundary and "**/cmd/*.go" starts matching cmd/sub/deep.go.
//
// Say plainly what this test can and cannot do. On Unix the two functions
// answer identically for every input this package produces, so swapping
// path.Match back for filepath.Match does not fail here — this records the
// contract and the last case says which way it points, but the regression
// it guards can only be caught on Windows. The job that runs this package
// there is what makes it load-bearing; the split above is the half that
// fails on any machine.
func TestTheSuffixMatchDoesNotLetAStarCrossADirectory(t *testing.T) {
	root := filepath.FromSlash("/ws")
	for _, c := range []struct {
		suffix string
		file   string
		want   bool
	}{
		{"*.go", filepath.FromSlash("/ws/main.go"), true},
		{"*.go", filepath.FromSlash("/ws/cmd/localcode/main.go"), true}, // via the segment loop
		{"localcode/*.go", filepath.FromSlash("/ws/cmd/localcode/main.go"), true},
		{"localcode/*.go", filepath.FromSlash("/ws/internal/agent/loop.go"), false},
		// The one the matcher choice decides: a star must not swallow a
		// directory of its own accord.
		{"cmd/*.go", filepath.FromSlash("/ws/cmd/sub/deep.go"), false},
	} {
		if got := globSuffixMatch(c.suffix, root, c.file); got != c.want {
			t.Errorf("globSuffixMatch(%q, %q) = %v, want %v", c.suffix, c.file, got, c.want)
		}
	}
}
