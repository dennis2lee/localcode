package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/events"
	"localcode/internal/memory"
	"localcode/internal/session"
)

// TestMemoryCommandDisabled confirms "/memory" answers locally (no model
// call — the test loop's model server is left unset, so a chat request
// would fail) when MemoryDir is unset, i.e. auto memory is off.
func TestMemoryCommandDisabled(t *testing.T) {
	loop, store := newCustomCommandTestLoop(t, "", nil)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/memory"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	text := lastMessagePartEnd(t, store, sid)
	if !strings.Contains(text, "disabled") {
		t.Errorf("text = %q, want it to mention auto memory is disabled", text)
	}
}

// TestMemoryCommandShowsEmptyIndex confirms "/memory" reports the memory
// dir/index paths and a "nothing saved yet" note when MemoryDir is set but
// MEMORY.md doesn't exist.
func TestMemoryCommandShowsEmptyIndex(t *testing.T) {
	loop, store := newCustomCommandTestLoop(t, "", nil)
	loop.MemoryDir = t.TempDir()
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/memory"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	text := lastMessagePartEnd(t, store, sid)
	if !strings.Contains(text, loop.MemoryDir) {
		t.Errorf("text = %q, want it to contain the memory dir %q", text, loop.MemoryDir)
	}
	if !strings.Contains(text, "No memory saved yet") {
		t.Errorf("text = %q, want it to note nothing is saved yet", text)
	}
}

// TestMemoryCommandShowsExistingIndex confirms "/memory" surfaces
// MEMORY.md's actual content when one exists.
func TestMemoryCommandShowsExistingIndex(t *testing.T) {
	loop, store := newCustomCommandTestLoop(t, "", nil)
	loop.MemoryDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(loop.MemoryDir, "MEMORY.md"), []byte("- prefers pnpm over npm"), 0o644); err != nil {
		t.Fatal(err)
	}
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/memory"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	text := lastMessagePartEnd(t, store, sid)
	if !strings.Contains(text, "prefers pnpm over npm") {
		t.Errorf("text = %q, want it to contain the saved index entry", text)
	}
}

// TestMemorySystemPromptSectionIncludesIndex is a sanity check that the
// memory package's own section-builder (used at daemon startup, not
// exercised by the Loop tests above) surfaces saved content — guards
// against the two call sites drifting apart.
func TestMemorySystemPromptSectionIncludesIndex(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("- fact"), 0o644); err != nil {
		t.Fatal(err)
	}
	section := memory.SystemPromptSection(dir, memory.LoadIndex(dir))
	if !strings.Contains(section, "fact") {
		t.Errorf("section = %q, want it to include the saved fact", section)
	}
}

func lastMessagePartEnd(t *testing.T, store *session.Store, sessionID string) string {
	t.Helper()
	all, err := store.Events(sessionID, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var text string
	for _, ev := range all {
		if ev.Type == events.TypeMessagePartEnd {
			text, _ = ev.Data["text"].(string)
		}
	}
	return text
}
