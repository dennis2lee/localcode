package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"localcode/internal/events"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// A background task's question is written to the task first, and to the
// conversation it is mirrored into only once that has worked.
//
// The mirror used to go first. When the task's own write then failed, the
// call returned the error with the mirror already in the conversation: a
// question nothing would ever resolve, shown live to whoever was
// watching, and answered 404. Its pending entry outlived the call.
//
// The task's write is made to fail with a log this process cannot read:
// the store refuses to append to a log it could not load.
func TestAFailedTaskWriteMirrorsNoQuestion(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a file this process can write and not read")
	}
	dir := t.TempDir()
	meta := map[string]string{
		"conv": `{"id":"conv","visible":true,"agent":"general-purpose","created_at":"2026-01-01T00:00:00Z"}`,
		"task": `{"id":"task","parent_id":"conv","visible":false,"agent":"general-purpose","created_at":"2026-01-01T00:00:00Z"}`,
	}
	for id, body := range meta {
		if err := os.WriteFile(filepath.Join(dir, id+".meta.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "conv.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	taskLog := filepath.Join(dir, "task.jsonl")
	if err := os.WriteFile(taskLog, []byte("{}\n"), 0o200); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(taskLog, 0o600) })
	store, _, err := session.LoadAllFromDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	b := NewPermissionBroker(store)

	ok, askErr := b.Func()(WithSessionID(context.Background(), "task"),
		tools.Ask{Tool: "bash", Subject: "go test ./...", Description: "go test ./..."})
	if ok || askErr == nil {
		t.Fatalf("the task's write was made to fail, and the call returned allow=%v err=%v", ok, askErr)
	}

	evs, err := store.Events("conv", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Type == events.TypePermissionRequest {
			t.Errorf("the conversation holds a mirrored question %v that nothing will resolve", ev.Data["id"])
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) != 0 || len(b.shownIn) != 0 {
		t.Errorf("the failed call left %d pending and %d shown entries behind", len(b.pending), len(b.shownIn))
	}
}
