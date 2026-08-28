package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// glob was the one file tool with no permission subject at all, so no
// rule could match it and the boundary could not see it: a pattern
// aimed at another project listed its files and nothing asked.
func TestGlobSubjectIsTheDirectoryBeingListed(t *testing.T) {
	for _, tt := range []struct{ pattern, want string }{
		{"src/**/*.go", "src"},
		{"/etc/*.conf", "/etc"},
		{"*.go", "."},
		{"internal/tools/read.go", "internal/tools/read.go"},
		{"../other/**/*.go", "../other"},
		{"", ""},
	} {
		if got := globSubject(tt.pattern); got != tt.want {
			t.Errorf("globSubject(%q) = %q, want %q", tt.pattern, got, tt.want)
		}
	}
	// And the tool actually exposes it, which is the half a rule and the
	// boundary both depend on.
	var g Glob
	got := g.Subject(json.RawMessage(`{"pattern":"/etc/*.conf"}`))
	if got != "/etc" {
		t.Errorf("Glob.Subject = %q, want %q", got, "/etc")
	}
	var gr Grep
	if got := gr.Subject(json.RawMessage(`{"pattern":"x"}`)); got != "." {
		t.Errorf("Grep.Subject with no path = %q, want the workspace itself", got)
	}
}

// The unit of consent is a directory, and a directory means its tree.
// Being asked again one level down inside a directory that was just
// approved is the same keystroke problem in a smaller box.
func TestOutsideDirAndUnderDir(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	file := filepath.Join(sub, "x.go")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	ctx := context.Background()

	// A file is answered by its directory; a directory by itself; a file
	// that does not exist yet by the directory it would land in, which is
	// the ordinary case for a write.
	if got := OutsideDir(ctx, file); !samePath(got, sub) {
		t.Errorf("OutsideDir(file) = %q, want %q", got, sub)
	}
	if got := OutsideDir(ctx, sub); !samePath(got, sub) {
		t.Errorf("OutsideDir(dir) = %q, want the directory itself", got)
	}
	if got := OutsideDir(ctx, filepath.Join(sub, "new.go")); !samePath(got, sub) {
		t.Errorf("OutsideDir(missing file) = %q, want %q", got, sub)
	}

	if !UnderDir(root, file) {
		t.Error("a file two levels down was not under the directory that contains it")
	}
	if !UnderDir(sub, sub) {
		t.Error("a directory was not under itself")
	}
	if UnderDir(sub, filepath.Join(root, "elsewhere", "y.go")) {
		t.Error("a sibling directory was treated as under this one")
	}
	if UnderDir("", file) || UnderDir(sub, "") {
		t.Error("an empty side granted something")
	}
}

func samePath(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	if err1 == nil && err2 == nil {
		return ra == rb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
