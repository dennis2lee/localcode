package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBashSuccess(t *testing.T) {
	input, _ := json.Marshal(map[string]string{"command": "echo hello"})
	result := Bash{}.Execute(context.Background(), input)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if strings.TrimSpace(result.Content) != "hello" {
		t.Errorf("output = %q, want %q", result.Content, "hello")
	}
}

func TestBashNonZeroExit(t *testing.T) {
	input, _ := json.Marshal(map[string]string{"command": "exit 1"})
	result := Bash{}.Execute(context.Background(), input)
	if !result.IsError {
		t.Error("expected an error for a nonzero exit command")
	}
}

func TestBashTimeout(t *testing.T) {
	b := Bash{Timeout: 50 * time.Millisecond}
	input, _ := json.Marshal(map[string]string{"command": "sleep 2"})

	start := time.Now()
	result := b.Execute(context.Background(), input)
	elapsed := time.Since(start)

	if !result.IsError {
		t.Error("expected a timeout error")
	}
	if elapsed > time.Second {
		t.Errorf("expected the command to be killed near the timeout, took %v", elapsed)
	}
}

func TestBashRequiresPermission(t *testing.T) {
	if !(Bash{}.RequiresPermission(nil)) {
		t.Error("bash should always require permission")
	}
}
