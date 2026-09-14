package daemon

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every route the daemon registers is named in docs/API.md.
//
// The weakest useful guard, and deliberately so. It cannot tell a route
// documented correctly from one merely mentioned, and it does not try —
// what it catches is the case that actually happened to every other
// hand-maintained list in this repository: a route added to the mux, used
// by a client, and missing from the page, which breaks nothing and rots
// for two releases because nothing fails.
//
// The registered list comes from daemon.go's own HandleFunc lines, not
// from a second list somebody maintains beside them: a second list is
// the same staleness with an extra step. docs/API.md must name each
// route as "METHOD /path" in a code span, so the match is on the method
// plus the full path plus the closing backtick — without the backtick,
// "GET /api/sessions/{id}" would match inside
// "GET /api/sessions/{id}/effort" and the test would pass with the
// shorter route undocumented.
func TestEveryRouteIsDocumentedInTheAPIReference(t *testing.T) {
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "internal", "daemon", "daemon.go"))
	if err != nil {
		t.Fatalf("reading daemon.go: %v", err)
	}
	// d.mux.HandleFunc("POST /api/sessions", d.handleCreateSession)
	registered := regexp.MustCompile(`HandleFunc\("([A-Z]+ /api/[^"]+)"`).FindAllStringSubmatch(string(src), -1)
	if len(registered) == 0 {
		t.Fatalf("found no registered routes in daemon.go — the scan is broken, not the coverage")
	}

	doc, err := os.ReadFile(filepath.Join(root, "docs", "API.md"))
	if err != nil {
		t.Fatalf("reading docs/API.md: %v", err)
	}
	body := string(doc)

	documented := func(route string) bool {
		return strings.Contains(body, "`"+route+"`")
	}

	// A negative control, checked first. A matcher that reports every
	// route as documented would pass this test with no reference page at
	// all, which is the failure this whole file exists to rule out.
	if documented("GET /api/not-a-real-route-negative-control") {
		t.Fatalf("the matcher claims a route that does not exist is documented — it matches too much to prove anything")
	}

	seen := map[string]bool{}
	for _, m := range registered {
		route := m[1]
		if seen[route] {
			continue
		}
		seen[route] = true
		if !documented(route) {
			t.Errorf("%s is registered in daemon.go and not documented in docs/API.md", route)
		}
	}
}

// repoRoot lives in events_guard_test.go and is reused here: one helper
// that finds the tree root, not one per guard file.
