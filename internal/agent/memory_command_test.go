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
// would fail) when auto memory is off.
func TestMemoryCommandDisabled(t *testing.T) {
	loop, store := newCustomCommandTestLoop(t, "", nil)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for _, line := range []string{"/memory", "/memory tidy"} {
		if err := loop.SendMessage(context.Background(), sid, "general-purpose", line); err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
		if text := lastMessagePartEnd(t, store, sid); !strings.Contains(text, "disabled") {
			t.Errorf("%s: text = %q, want it to say auto memory is disabled", line, text)
		}
	}
}

// memoryTestLoop is the custom-command loop with memory on, its project
// store in a directory of its own and its user store under a home of its
// own.
func memoryTestLoop(t *testing.T, modelURL string) (*Loop, string) {
	t.Helper()
	loop, store := newCustomCommandTestLoop(t, modelURL, nil)
	loop.Memory = &MemoryConfig{Home: t.TempDir(), ProjectDir: filepath.Join(t.TempDir(), "memory"), CountSessions: true, RetireDays: 90}
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return loop, sid
}

// TestMemoryCommandShowsEmptyIndex confirms "/memory" reports where each
// scope lives and a "nothing saved yet" note when nothing is.
func TestMemoryCommandShowsEmptyIndex(t *testing.T) {
	loop, sid := memoryTestLoop(t, "")
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/memory"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	text := lastMessagePartEnd(t, loop.Store, sid)
	for _, want := range []string{loop.Memory.ProjectDir, memory.UserDir(loop.Memory.Home), "Session memory", "No memory saved yet", "/memory tidy"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	// Showing creates nothing.
	if _, err := os.Stat(loop.Memory.ProjectDir); !os.IsNotExist(err) {
		t.Errorf("/memory created the project directory: %v", err)
	}
}

// TestMemoryCommandShowsExistingIndex confirms "/memory" surfaces what
// the model is shown, a hand-written MEMORY.md included.
func TestMemoryCommandShowsExistingIndex(t *testing.T) {
	loop, sid := memoryTestLoop(t, "")
	if err := os.MkdirAll(loop.Memory.ProjectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(loop.Memory.ProjectDir, "MEMORY.md"), []byte("- prefers pnpm over npm"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/memory"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	text := lastMessagePartEnd(t, loop.Store, sid)
	if !strings.Contains(text, "prefers pnpm over npm") {
		t.Errorf("text = %q, want it to contain the saved index entry", text)
	}
}

func TestMemoryTidySendsTheConsolidationRequest(t *testing.T) {
	loop, sid, bodies := effortLoop(t, "")
	loop.Memory = &MemoryConfig{Home: t.TempDir(), ProjectDir: filepath.Join(t.TempDir(), "memory")}
	if err := loop.SendMessage(context.Background(), sid, "boy", "/memory tidy"); err != nil {
		t.Fatal(err)
	}
	got := bodies()
	if len(got) != 1 {
		t.Fatalf("%d requests, want 1", len(got))
	}
	raw := strings.Join(messageTexts(got[0]), "\n")
	for _, want := range []string{"Tidy your memory", "one note at a time", "archive"} {
		if !strings.Contains(raw, want) {
			t.Errorf("the request lacks %q", want)
		}
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
