package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The completion list, checked in the direction the other three do not.
//
// SlashCommands is hand-written, and three tests beside it already check
// that everything in it is real: answered by a route, described, and
// documented. All three run from the list outwards, so all three are
// silent about the failure that actually happens, which is a command
// added to the router and forgotten here. Nothing breaks, nothing fails,
// and the command is simply uncompletable in both clients for as long as
// nobody notices. "/orchestrate" was one keystroke from being that.
//
// The list cannot be derived from commandRoutes, which holds closures and
// no names, so this reads the source instead: every string literal shaped
// like a command in every non-test file of this package. The technique is
// the repository's own, the same one the process-spawn walk uses, and a
// comment cannot be a literal so prose about "/name" does not count.
//
// It needs no allowlist today: every one of the literals it finds is a
// command. If a future one is not, the fix is to excuse it here
// deliberately rather than to loosen the pattern, because a pattern loose
// enough to admit a path is loose enough to miss a command.
var commandLiteral = regexp.MustCompile(`^/[a-z][a-z0-9-]*$`)

func TestEveryCommandInTheSourceIsOfferedForCompletion(t *testing.T) {
	listed := map[string]bool{}
	for _, c := range SlashCommands() {
		listed[c.Name] = true
	}

	fset := token.NewFileSet()
	where := map[string]string{}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Errorf("parse %s: %v", path, perr)
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, uerr := strconv.Unquote(lit.Value)
			if uerr != nil || !commandLiteral.MatchString(v) {
				return true
			}
			if _, seen := where[v]; !seen {
				where[v] = fset.Position(lit.Pos()).String()
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	// A floor, so a regexp that stopped matching cannot pass as a clean
	// bill of health.
	if len(where) < 15 {
		t.Fatalf("found only %d command literals, so this test is checking almost nothing", len(where))
	}

	var missing []string
	for lit := range where {
		if !listed[strings.TrimPrefix(lit, "/")] {
			missing = append(missing, lit)
		}
	}
	sort.Strings(missing)
	for _, lit := range missing {
		t.Errorf("%s is written at %s and is not in SlashCommands(), so neither client can complete it",
			lit, where[lit])
	}
}
