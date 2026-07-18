package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileCreatesFileAndDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "out.txt")

	input, _ := json.Marshal(map[string]string{"path": path, "content": "hello world"})
	result := WriteFile{}.Execute(context.Background(), input)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back written file: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("file content = %q, want %q", data, "hello world")
	}
}

func TestWriteFileOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	input, _ := json.Marshal(map[string]string{"path": path, "content": "new"})
	result := WriteFile{}.Execute(context.Background(), input)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "new" {
		t.Errorf("file content = %q, want %q", data, "new")
	}
}

func TestWriteFileRequiresPermission(t *testing.T) {
	if !(WriteFile{}.RequiresPermission(nil)) {
		t.Error("write_file should always require permission")
	}
}

func TestWriteFileSubjectExposesPath(t *testing.T) {
	got := WriteFile{}.Subject(json.RawMessage(`{"path":"dist/out.js","content":"x"}`))
	if got != "dist/out.js" {
		t.Errorf("Subject() = %q, want %q", got, "dist/out.js")
	}
}
