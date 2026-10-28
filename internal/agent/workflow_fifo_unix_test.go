//go:build unix

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Opening a FIFO blocks until something writes to it, and a command
// blocked in an open cannot be cancelled. One named like a workflow is
// listed as not a regular file.
func TestAFIFONamedLikeAWorkflowIsListedNotOpened(t *testing.T) {
	loop, project, _, _ := workflowLoop(t, func(string) (string, map[string]any) { return "ok", nil })
	wdir := filepath.Join(project, ".localcode", "workflows")
	if err := os.MkdirAll(wdir, 0o755); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(wdir, "stuck.json")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}

	done := make(chan struct{})
	go func() {
		loop.SendMessage(context.Background(), "conv", "oracle", "/workflow list")
		close(done)
	}()
	select {
	case <-done:
		if out := lastAssistantText(loop.Store, "conv"); !strings.Contains(out, "not a regular file") {
			t.Errorf("/workflow list does not say why stuck cannot run:\n%s", out)
		}
	case <-time.After(3 * time.Second):
		// Unblock the reader so the goroutine ends with the test.
		if f, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			f.Close()
		}
		<-done
		t.Errorf("/workflow list was still blocked after 3s on a FIFO named %s", filepath.Base(fifo))
	}
}
