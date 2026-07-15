package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

func TestEditUniqueMatch(t *testing.T) {
	path := writeTemp(t, "hello world")
	input, _ := json.Marshal(map[string]string{"path": path, "old_string": "world", "new_string": "there"})

	result := Edit{}.Execute(context.Background(), input)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "hello there" {
		t.Errorf("content = %q, want %q", data, "hello there")
	}
}

func TestEditNotFound(t *testing.T) {
	path := writeTemp(t, "hello world")
	input, _ := json.Marshal(map[string]string{"path": path, "old_string": "goodbye", "new_string": "hi"})

	result := Edit{}.Execute(context.Background(), input)
	if !result.IsError {
		t.Error("expected an error when old_string isn't present")
	}
}

func TestEditAmbiguousWithoutReplaceAll(t *testing.T) {
	path := writeTemp(t, "foo foo foo")
	input, _ := json.Marshal(map[string]string{"path": path, "old_string": "foo", "new_string": "bar"})

	result := Edit{}.Execute(context.Background(), input)
	if !result.IsError {
		t.Error("expected an error for a non-unique old_string without replace_all")
	}

	data, _ := os.ReadFile(path)
	if string(data) != "foo foo foo" {
		t.Errorf("file should be unchanged on ambiguous match, got %q", data)
	}
}

func TestEditReplaceAll(t *testing.T) {
	path := writeTemp(t, "foo foo foo")
	input, _ := json.Marshal(map[string]any{"path": path, "old_string": "foo", "new_string": "bar", "replace_all": true})

	result := Edit{}.Execute(context.Background(), input)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "bar bar bar" {
		t.Errorf("content = %q, want %q", data, "bar bar bar")
	}
}

func TestEditRequiresPermission(t *testing.T) {
	if !(Edit{}.RequiresPermission(nil)) {
		t.Error("edit should always require permission")
	}
}
