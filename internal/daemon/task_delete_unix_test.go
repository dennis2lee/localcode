//go:build !windows

package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"localcode/internal/events"
)

// One task of several cannot be deleted: its row stays to be tried again,
// the others go, and the answer says which failed. (The failure path had
// no test; found by review.)
//
// Unix only: the failure is a directory this process may not write to,
// which Windows does not enforce the same way.
func TestDeleteAllKeepsTheRowOfATaskItCouldNotDelete(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission that makes the delete fail")
	}
	d, _, dir := handoffDaemon(t)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()
	if _, err := d.Loop.Store.CreateSession("parent", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"task-ok", "task-stuck"} {
		if _, err := d.Loop.Store.CreateSessionIn(id, "parent", "build", "", false); err != nil {
			t.Fatal(err)
		}
		d.Loop.Store.Append("parent", events.TypeTaskSpawned, map[string]any{"task_id": id, "agent": "build"})
	}
	// A checkpoint file inside a directory nothing may write to.
	locked := filepath.Join(dir, "task-stuck.checkpoints", "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "pre-image"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	code, body := deleteRequest(t, srv.URL+"/api/sessions/parent/tasks")
	if code != http.StatusInternalServerError {
		t.Fatalf("status %d (%s), want 500", code, body)
	}
	if rows := rowsOf(t, d, "parent"); !reflect.DeepEqual(rows, []string{"task-stuck"}) {
		t.Errorf("rows %v, want only the one that could not be deleted", rows)
	}
	if _, err := d.Loop.Store.Get("task-ok"); err == nil {
		t.Error("the task that could be deleted was not")
	}
}
