package daemon

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every `app.<name>` and `session.<name>` the Web UI touches has to be a
// key state.js declares.
//
// Neither object is a class and neither is frozen, so reading an
// undeclared key yields undefined and writing one succeeds: both failure
// modes are silent, and both have happened.
//
//   - Reading. The permissions panel read `app.workspace`, which is not a
//     field -- the real one is `workspacePath` -- so the one panel whose
//     entire subject is a directory never named one. Fixed in 1b4c0cf
//     after v0.63.0.
//   - Writing. events.js set `session.pendingPermissionOutside` on every
//     permission request. Nothing declared it and nothing read it, so it
//     was a dead assignment that resetSession could not clear either;
//     had anyone come to read it, a value from a previous conversation
//     would have been waiting. Removed with this test.
//
// The comment on freshSessionState says the object being the reset unit
// is the point, "instead of depending on someone remembering". This is
// what stops that from depending on someone remembering.
func TestEveryStateKeyTheWebUIUsesIsDeclared(t *testing.T) {
	dir := filepath.Join("static", "js")
	declared, err := declaredStateKeys(filepath.Join(dir, "state.js"))
	if err != nil {
		t.Fatalf("%v", err)
	}
	for _, obj := range []string{"app", "session"} {
		if len(declared[obj]) < 5 {
			t.Fatalf("only %d keys parsed for `%s` from state.js, so this test is checking almost nothing", len(declared[obj]), obj)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	// `app.foo` / `session.foo`, and none of the three things that look
	// like one:
	//   - `.session.foo`, a property named session on something else;
	//   - `app.foo(`, a method call, which is a helper defined elsewhere;
	//   - `'session.forked'`, an SSE event name. events.js is a map keyed
	//     by those, so leaving quotes out of this reported three event
	//     types as undeclared state on the first run.
	use := regexp.MustCompile("(^|[^.\\w'\"`])(app|session)\\.([A-Za-z_$][\\w$]*)\\s*(\\()?")

	var problems []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".js") || e.Name() == "state.js" {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, m := range use.FindAllStringSubmatch(stripJSComments(string(src)), -1) {
			obj, key, isCall := m[2], m[3], m[4] == "("
			if isCall || declared[obj][key] {
				continue
			}
			problems = append(problems, e.Name()+": "+obj+"."+key)
		}
	}

	sort.Strings(problems)
	seen := map[string]bool{}
	for _, p := range problems {
		if seen[p] {
			continue
		}
		seen[p] = true
		t.Errorf("%s is not a key state.js declares: reading it yields undefined, and writing it makes state that resetSession cannot clear", p)
	}
}

// declaredStateKeys reads the keys of the `app` object literal and of the
// object freshSessionState returns.
//
// Only top-level keys, found by brace depth: a nested literal such as
// sessionPermissions declares its own keys, and those are not reachable
// as `app.<name>`.
func declaredStateKeys(path string) (map[string]map[string]bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := stripJSComments(string(src))

	out := map[string]map[string]bool{}
	for _, spec := range []struct{ obj, opener string }{
		{"app", "export const app = {"},
		{"session", "return {"},
	} {
		i := strings.Index(text, spec.opener)
		if i < 0 {
			return nil, errNoStateBlock(spec.opener)
		}
		out[spec.obj] = topLevelKeys(text[i+len(spec.opener):])
	}
	return out, nil
}

// topLevelKeys walks an object literal body line by line, stopping at the
// brace that closes it, and collects `name:` where a line starts at depth
// zero.
//
// The key is taken at the START of a line, before that line's own
// brackets are counted. Deciding at the end of the line instead is what
// the first version did, and on
//
//	sessionPermissions: { skip_all: false, ..., write_outside: false },
//
// it came back with `write_outside` -- the last fragment left in the
// buffer once the nested literal had opened and closed -- and no
// `sessionPermissions` at all, so two real keys were reported as
// undeclared.
func topLevelKeys(body string) map[string]bool {
	keys := map[string]bool{}
	key := regexp.MustCompile(`^\s*([A-Za-z_$][\w$]*)\s*:`)

	depth := 0
	for _, line := range strings.Split(body, "\n") {
		if depth == 0 {
			if m := key.FindStringSubmatch(line); m != nil {
				keys[m[1]] = true
			}
		}
		for _, r := range line {
			switch r {
			case '{', '[', '(':
				depth++
			case '}', ']', ')':
				if depth == 0 && r == '}' {
					return keys
				}
				depth--
			}
		}
	}
	return keys
}

// stripJSComments blanks line and block comments so a key named in prose
// is not mistaken for one in code. Length is not preserved; nothing here
// needs offsets.
func stripJSComments(s string) string {
	s = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(s, "")
	return regexp.MustCompile(`(?m)^\s*//.*$|\s//[^\n]*$`).ReplaceAllString(s, "")
}

type errNoStateBlock string

func (e errNoStateBlock) Error() string {
	return "no `" + string(e) + "` in static/js/state.js: this guard reads the state objects by that literal, so reshaping them needs this test updated rather than left to pass on nothing"
}
