package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	input, _ := json.Marshal(map[string]string{"path": path})
	result := ReadFile{}.Execute(context.Background(), input)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "1\tline one") {
		t.Errorf("expected line-numbered output, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "2\tline two") {
		t.Errorf("expected line-numbered output, got: %q", result.Content)
	}
}

func TestReadFileMissing(t *testing.T) {
	input, _ := json.Marshal(map[string]string{"path": "/nonexistent/path/does/not/exist.txt"})
	result := ReadFile{}.Execute(context.Background(), input)
	if !result.IsError {
		t.Error("expected an error reading a nonexistent file")
	}
}

func TestReadFileRequiresNoPermission(t *testing.T) {
	rf := ReadFile{}
	if rf.RequiresPermission(nil) {
		t.Error("read_file should never require permission")
	}
}
