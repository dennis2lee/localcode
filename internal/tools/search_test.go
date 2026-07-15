package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobDoubleStar(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, content string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	mustWrite("a.go", "package a")
	mustWrite("sub/b.go", "package b")
	mustWrite("sub/c.txt", "not go")

	input, _ := json.Marshal(map[string]string{"pattern": dir + "/**/*.go"})
	result := Glob{}.Execute(context.Background(), input)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "a.go") {
		t.Errorf("expected a.go in results, got: %q", result.Content)
	}
	if !strings.Contains(result.Content, "b.go") {
		t.Errorf("expected sub/b.go in results, got: %q", result.Content)
	}
	if strings.Contains(result.Content, "c.txt") {
		t.Errorf("did not expect c.txt in .go glob results, got: %q", result.Content)
	}
}

func TestGlobRequiresNoPermission(t *testing.T) {
	g := Glob{}
	if g.RequiresPermission(nil) {
		t.Error("glob should never require permission")
	}
}

func TestGrepFindsMatches(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\nTODO: fix this\nworld\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("nothing interesting\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	input, _ := json.Marshal(map[string]string{"pattern": "TODO", "path": dir})
	result := Grep{}.Execute(context.Background(), input)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if !strings.Contains(result.Content, "a.txt") || !strings.Contains(result.Content, "TODO: fix this") {
		t.Errorf("expected match from a.txt, got: %q", result.Content)
	}
	if strings.Contains(result.Content, "b.txt") {
		t.Errorf("did not expect b.txt to match, got: %q", result.Content)
	}
}

func TestGrepNoMatches(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("nothing here\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	input, _ := json.Marshal(map[string]string{"pattern": "NOPE_NOT_FOUND", "path": dir})
	result := Grep{}.Execute(context.Background(), input)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if result.Content != "no matches" {
		t.Errorf("content = %q, want %q", result.Content, "no matches")
	}
}

func TestGrepInvalidRegex(t *testing.T) {
	dir := t.TempDir()
	input, _ := json.Marshal(map[string]string{"pattern": "(unclosed", "path": dir})
	result := Grep{}.Execute(context.Background(), input)
	if !result.IsError {
		t.Error("expected an error for an invalid regex")
	}
}

func TestGrepRequiresNoPermission(t *testing.T) {
	g := Grep{}
	if g.RequiresPermission(nil) {
		t.Error("grep should never require permission")
	}
}
