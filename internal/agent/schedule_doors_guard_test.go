package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The places that book a prompt, and who wrote what they book.
//
// Scheduler.Add is for a prompt a person wrote and AddByModel for one the
// model did, and nothing but the caller knows which. The run that fires a
// booking hands its prompt to SendMessage, which treats a leading "/" or
// "!" as a command unless it was told the text was not typed, so a caller
// that books the model's words with Add has reopened the defect
// schedule_author_test.go is about, and nothing fails until somebody
// notices a shell command that nobody typed.
//
// So the callers are listed. The list is short on purpose, and a new entry
// is a decision that wants a reader: the question is whose words the
// prompt holds, and the answer is not "whoever called it".
//
// The technique is the repository's own: the source is read rather than
// the behaviour exercised, because the failure is a caller nothing tests.
// It reads the selector shape `<anything>.Schedules.Add`, so a method
// value counts as a call, and it needs no type information.
func TestEveryBookingDoorSaysWhoWroteThePrompt(t *testing.T) {
	// What each door is, and whose words it books.
	//
	//   Execute        the Schedule tool: the model's.
	//   routeSchedule  a "/schedule" line: a person's typed line, and the
	//                  model's when the Command tool is the one running it.
	//                  It calls both, on the same condition the Command
	//                  tool's mark is read under.
	//   handleBookSchedule  the window's Schedule dialog, which a person
	//                  fills in: theirs.
	want := []string{
		"internal/agent/schedule_tool.go ScheduleTool.Execute AddByModel",
		"internal/agent/toggles.go Loop.routeSchedule Add",
		"internal/agent/toggles.go Loop.routeSchedule AddByModel",
		"internal/daemon/schedules.go Daemon.handleBookSchedule Add",
	}

	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	var got []string
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				t.Errorf("parse %s: %v", path, perr)
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					sel, ok := n.(*ast.SelectorExpr)
					if !ok || (sel.Sel.Name != "Add" && sel.Sel.Name != "AddByModel") {
						return true
					}
					if inner, ok := sel.X.(*ast.SelectorExpr); !ok || inner.Sel.Name != "Schedules" {
						return true
					}
					got = append(got, filepath.ToSlash(rel)+" "+funcName(fn)+" "+sel.Sel.Name)
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}

	// A floor, so a walk that found nothing cannot pass for a clean bill.
	if len(got) == 0 {
		t.Fatal("found no booking doors, so this test is checking nothing")
	}
	sort.Strings(got)
	sort.Strings(want)
	have := map[string]bool{}
	for _, g := range got {
		have[g] = true
	}
	listed := map[string]bool{}
	for _, w := range want {
		listed[w] = true
		if !have[w] {
			t.Errorf("%s is listed and the source no longer does that: update the list", w)
		}
	}
	for _, g := range got {
		if !listed[g] {
			t.Errorf("%s books a prompt and is not listed. Decide whose words the prompt holds: "+
				"Add for a person's, AddByModel for the model's, and then list the door here", g)
		}
	}
}

// funcName is "Type.Method" for a method and the bare name for a function,
// so two methods of the same name on different types are told apart.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	typ := fn.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}
