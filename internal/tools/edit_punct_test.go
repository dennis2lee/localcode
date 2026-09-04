package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A model that quotes a curly apostrophe where the file has a straight one
// has written a string that is otherwise perfect. "not found" does not say
// which character to change; the diagnosis does, and it quotes the file's
// own bytes rather than applying the edit.
func TestEditDiagnosisNamesAPunctuationMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	const real = "package main\n\n// don't fold - this line\nfunc main() {}\n"
	if err := os.WriteFile(path, []byte(real), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := WithSmartAgent(WithWorkingDir(context.Background(), dir), true)
	in, _ := json.Marshal(map[string]any{
		"path": path, "old_string": "// don’t fold – this line", "new_string": "// ok",
	})
	got := Edit{}.Execute(ctx, in)

	if !got.IsError {
		t.Fatal("a punctuation-different string must not be applied")
	}
	for _, want := range []string{"line 3", "punctuation", "don't fold - this line"} {
		if !strings.Contains(got.Content, want) {
			t.Errorf("diagnosis lacks %q:\n%s", want, got.Content)
		}
	}
	if after, _ := os.ReadFile(path); string(after) != real {
		t.Errorf("the file was written; the fold is for finding the line, not for editing:\n%s", after)
	}
}

// The fold is only reached when whitespace alone does not explain the
// miss, and it never overrides the whitespace diagnosis.
func TestWhitespaceDiagnosisStillWinsFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.go")
	if err := os.WriteFile(path, []byte("func f() {\n\treturn 1\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := WithSmartAgent(WithWorkingDir(context.Background(), dir), true)
	in, _ := json.Marshal(map[string]any{
		"path": path, "old_string": "func f() {\n    return 1\n}", "new_string": "x",
	})
	got := Edit{}.Execute(ctx, in)
	if !strings.Contains(got.Content, "whitespace") {
		t.Errorf("reply = %q, want the whitespace diagnosis", got.Content)
	}
}
