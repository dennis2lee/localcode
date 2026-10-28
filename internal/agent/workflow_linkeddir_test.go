package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A cloned repository can ship .localcode/workflows as a symlink to any
// directory, the home's own workflows included. Save and delete without
// --user write only in the project's own directory, so through a link
// they refuse; the home's file is deleted with --user or not at all.
func TestSaveAndDeleteRefuseALinkedProjectWorkflowDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	loop, project, home, _ := workflowLoop(t, func(string) (string, map[string]any) { return "ok", nil })
	homeFile := writeWorkflow(t, home, "mine", workflowBody("mine", "my own"))
	if err := os.MkdirAll(filepath.Join(project, ".localcode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".localcode", "workflows"), filepath.Join(project, ".localcode", "workflows")); err != nil {
		t.Skipf("symlink: %v", err)
	}

	loop.SendMessage(context.Background(), "conv", "oracle", "/workflow")
	list := lastAssistantText(loop.Store, "conv")

	loop.SendMessage(context.Background(), "conv", "oracle", "/workflow delete mine")
	out := lastAssistantText(loop.Store, "conv")
	if _, err := os.Stat(homeFile); os.IsNotExist(err) {
		t.Errorf("a project delete removed the home directory's %s through the project's symlinked directory\nlist said: %s\ndelete said: %s",
			homeFile, strings.TrimSpace(list), out)
	}

	// Save, after a run so there is a plan to save, refuses the same way
	// and writes nothing through the link.
	if res := NewOrchestrateTool(loop).Execute(WithSessionID(loop.pinSmart(context.Background()), "conv"),
		json.RawMessage(`{"goal":"g","stages":[{"name":"a","kind":"step","agent":"oracle","prompt":"x"}]}`)); res.IsError {
		t.Fatalf("the run failed: %s", res.Content)
	}
	loop.SendMessage(context.Background(), "conv", "oracle", "/workflow save planted")
	if out := lastAssistantText(loop.Store, "conv"); !strings.Contains(out, "symbolic link") {
		t.Errorf("a project save through a linked directory was not refused: %q", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".localcode", "workflows", "planted.json")); err == nil {
		t.Error("a project save wrote into the home's workflows through the link")
	}
}
