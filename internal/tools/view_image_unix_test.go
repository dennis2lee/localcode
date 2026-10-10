//go:build !windows

package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A named pipe has no size to check, and opening one with no writer blocks
// until somebody writes. It is refused before it is opened.
func TestViewImageRefusesAPipe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipe.png")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	input, _ := json.Marshal(map[string]string{"path": path})
	done := make(chan Result, 1)
	go func() { done <- ViewImage{}.Execute(context.Background(), input) }()
	select {
	case res := <-done:
		if !res.IsError || len(res.Images) != 0 || !strings.Contains(res.Content, "not a regular file") {
			t.Errorf("a pipe was not refused: %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("view_image blocked on a pipe with no writer")
	}
}

// A device is not a regular file either: /dev/zero never ends, and
// /dev/null is nothing. Both are refused before they are read.
func TestViewImageRefusesADevice(t *testing.T) {
	for _, path := range []string{"/dev/null", "/dev/zero"} {
		res := viewImage(t, context.Background(), path)
		if !res.IsError || len(res.Images) != 0 || !strings.Contains(res.Content, "not a regular file") {
			t.Errorf("%s was not refused as a device: %+v", path, res)
		}
	}
}
