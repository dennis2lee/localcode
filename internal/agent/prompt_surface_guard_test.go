package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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

// The other direction: the inventory and the code must name the same
// set of runtime entries, in both directions.
//
// The first version of this checked one direction with a substring
// search, and it was weak in three ways the round 14 review and its
// auditors between them found all of: a documented prefix was satisfied
// by the same characters appearing inside a comment, an emitted id with
// no row was invisible because nothing looked that way, and it read
// only this package while prompt.RuntimeEntry is exported.
//
// So the ids are taken from the syntax rather than from the text: the
// first argument of every prompt.RuntimeEntry call in every non-test
// file of the repository, reduced to the literal prefix the call can
// produce. A comment cannot be a call, and a caller in another package
// cannot hide.
func TestTheInventoryAndTheCodeNameTheSameEntries(t *testing.T) {
	documented := map[string]bool{}
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "PROMPT_ASSET_INVENTORY.md"))
	if err != nil {
		t.Fatalf("read the inventory: %v", err)
	}
	for _, line := range runtimeEntryRows(string(doc)) {
		id := strings.Trim(strings.TrimSpace(strings.Split(line, "|")[1]), "`")
		if id == "" {
			continue
		}
		documented[idPrefix(id)] = true
	}

	emitted := emittedEntryPrefixes(t)

	for prefix := range documented {
		if !emitted[prefix] {
			t.Errorf("the inventory documents %q and no non-test code builds an id starting that way", prefix)
		}
	}
	for prefix := range emitted {
		if !documented[prefix] {
			t.Errorf("non-test code builds ids starting %q and the inventory has no row for them", prefix)
		}
	}
	if len(documented) != len(emitted) {
		t.Errorf("%d documented runtime patterns against %d emitted", len(documented), len(emitted))
	}
	if len(emitted) < 10 {
		t.Fatalf("found only %d emitted entry ids, so this test is checking almost nothing", len(emitted))
	}
}

// idPrefix reduces a documented id pattern to the literal part the code
// can produce: "tool.mcp." for "tool.mcp.<name>", "conversation" for a
// pattern with no placeholder at all.
func idPrefix(id string) string {
	if i := strings.IndexAny(id, "<[#"); i >= 0 {
		return id[:i]
	}
	return id
}

// emittedEntryPrefixes finds the id literal of every prompt.RuntimeEntry
// call in the repository's non-test files.
//
// Syntax, not text. A call's first argument is either a string literal
// or a concatenation starting with one, and that leading literal is the
// prefix every id it produces begins with.
func emittedEntryPrefixes(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" || name == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 || !isRuntimeEntryCall(call.Fun) {
				return true
			}
			if lit, ok := leadingStringLiteral(call.Args[0]); ok {
				out[idPrefix(lit)] = true
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk the repository: %v", err)
	}
	return out
}

// isRuntimeEntryCall reports whether an expression is prompt.RuntimeEntry
// or, inside the prompt package itself, RuntimeEntry.
func isRuntimeEntryCall(fun ast.Expr) bool {
	switch f := fun.(type) {
	case *ast.SelectorExpr:
		id, ok := f.X.(*ast.Ident)
		return ok && id.Name == "prompt" && f.Sel.Name == "RuntimeEntry"
	case *ast.Ident:
		return f.Name == "RuntimeEntry"
	}
	return false
}

// leadingStringLiteral returns the literal an id expression starts with,
// which for "prefix."+name is "prefix." and for a plain literal is the
// whole of it.
func leadingStringLiteral(e ast.Expr) (string, bool) {
	for {
		switch v := e.(type) {
		case *ast.BasicLit:
			if v.Kind != token.STRING {
				return "", false
			}
			s, err := strconv.Unquote(v.Value)
			return s, err == nil
		case *ast.BinaryExpr:
			e = v.X
		default:
			return "", false
		}
	}
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

// runtimeEntryRows returns the body rows of the runtime entry table.
//
// Taken by section rather than by matching known id prefixes, which was
// an earlier version and was wrong in the way that matters: a row in a
// namespace the test had not been told about would have been skipped
// silently, so the one case this exists to catch would have passed.
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

// packageSource concatenates this package's non-test files.
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
