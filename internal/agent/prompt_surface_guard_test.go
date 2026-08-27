package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The inventory is a maintained list rather than something derived from
// the code, which is stated plainly in the document itself. That is the
// honest description and also the weak point: a constructor can be
// written, unit tested, and never attached to anything, and the
// inventory will still claim the entry exists. It happened twice, and
// both times a reviewer found it rather than a test.
//
// These two checks are what close that gap from the code side. Neither
// can tell whether an entry is attached at the *right* place, which is
// still a question for a person. They can tell whether it is attached
// at all, and whether the document is describing something that exists.

// entryConstructorsAreReachable fails when a function that builds a
// prompt entry is never called from anything except another entry
// constructor.
//
// Constructors calling constructors is normal and correct here:
// toolResultEntry routes a delegation to childResultEntry, and
// delegationInputEntry wraps childInputEntry. So reachability is what
// is checked, not a direct call. What must exist somewhere in the chain
// is one caller that is doing something other than building entries,
// which is exactly what "attached to a real path" means.
func TestEveryEntryConstructorIsCalledFromARealPath(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}

	constructors := map[string]bool{}
	calls := map[string][]string{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Name == nil || fn.Body == nil {
					continue
				}
				// A method that returns entries is a real path, not a
				// constructor: runTools produces them as part of running
				// tools. Only plain functions are candidates.
				if fn.Recv == nil && buildsAnEntry(fn) {
					constructors[fn.Name.Name] = true
				}
				calls[fn.Name.Name] = append(calls[fn.Name.Name], calleeNames(fn)...)
			}
		}
	}
	if len(constructors) == 0 {
		t.Fatal("found no entry constructors, so this test is checking nothing")
	}

	// Reachable from a caller that is not itself a constructor.
	reachable := map[string]bool{}
	var mark func(name string)
	mark = func(name string) {
		for _, callee := range calls[name] {
			if !constructors[callee] || reachable[callee] {
				continue
			}
			reachable[callee] = true
			mark(callee)
		}
	}
	for name := range calls {
		if constructors[name] {
			continue
		}
		mark(name)
	}

	for name := range constructors {
		if !reachable[name] {
			t.Errorf("%s builds a prompt entry and nothing outside the entry constructors calls it: "+
				"it is a constructor with a unit test, not an attached entry", name)
		}
	}
}

// buildsAnEntry reports whether a function's results include a prompt
// entry, in either the single or the slice form.
func buildsAnEntry(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil {
		return false
	}
	for _, res := range fn.Type.Results.List {
		typ := res.Type
		if arr, ok := typ.(*ast.ArrayType); ok {
			typ = arr.Elt
		}
		sel, ok := typ.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Entry" {
			continue
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == "prompt" {
			return true
		}
	}
	return false
}

// calleeNames lists the plain function names a body calls. Method calls
// and qualified calls into other packages are not relevant: an entry
// constructor in this package is called by its bare name.
func calleeNames(fn *ast.FuncDecl) []string {
	var out []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok {
			out = append(out, id.Name)
		}
		return true
	})
	return out
}

// The other direction: an inventory row describing an entry the code
// does not build. The document is the thing a reviewer reads, so a row
// with nothing behind it is worse than a missing row.
func TestTheInventoryDescribesEntriesThatExist(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "PROMPT_ASSET_INVENTORY.md")
	doc, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the inventory: %v", err)
	}
	source := packageSource(t)

	var checked int
	for _, line := range runtimeEntryRows(string(doc)) {
		id := strings.Trim(strings.TrimSpace(strings.Split(line, "|")[1]), "`")
		if id == "" {
			continue
		}
		checked++
		// The literal the code writes is the part before the first
		// placeholder: "tool.mcp." for "tool.mcp.<name>".
		prefix := id
		if i := strings.IndexAny(prefix, "<["); i >= 0 {
			prefix = prefix[:i]
		}
		if !strings.Contains(source, `"`+prefix) {
			t.Errorf("the inventory documents %q and no non-test source builds an id starting %q", id, prefix)
		}
	}
	if checked < 10 {
		t.Fatalf("recognised only %d runtime entry rows in the inventory, so this test is checking almost nothing", checked)
	}
}

// runtimeEntryRows returns the body rows of the runtime entry table.
//
// Taken by section rather than by matching known id prefixes, which was
// the first version and was wrong in the way that matters: a row in a
// namespace the test had not been told about would have been skipped
// silently, so the one case this exists to catch, somebody documenting
// an entry nothing builds, would have passed.
func runtimeEntryRows(doc string) []string {
	var out []string
	inSection, inTable := false, false
	for _, line := range strings.Split(doc, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			inSection = trimmed == "## Runtime entries"
			inTable = false
			continue
		}
		if !inSection || !strings.HasPrefix(trimmed, "|") {
			continue
		}
		// The header row, then the alignment row, then the body.
		if strings.HasPrefix(trimmed, "|---") {
			inTable = true
			continue
		}
		if inTable {
			out = append(out, trimmed)
		}
	}
	return out
}

func packageSource(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	var b strings.Builder
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		b.Write(data)
	}
	return b.String()
}

// And the third direction: a row citing a test that does not exist.
//
// The Test column is the part of the inventory a reviewer checks first,
// because it is the row's evidence. A stale name there is worse than an
// empty cell: it reads as verified and is not.
func TestTheInventoryCitesTestsThatExist(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "PROMPT_ASSET_INVENTORY.md"))
	if err != nil {
		t.Fatalf("read the inventory: %v", err)
	}
	source := packageSource(t) + testSource(t)

	cited := map[string]bool{}
	for _, m := range regexp.MustCompile("`(Test[A-Za-z0-9_]+)`").FindAllStringSubmatch(string(doc), -1) {
		cited[m[1]] = true
	}
	if len(cited) < 10 {
		t.Fatalf("found only %d cited tests, so this is checking almost nothing", len(cited))
	}
	for name := range cited {
		if !strings.Contains(source, "func "+name+"(") {
			t.Errorf("the inventory cites %s as evidence and no such test exists", name)
		}
	}
}

func testSource(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	var b strings.Builder
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		b.Write(data)
	}
	return b.String()
}
