package agent

import (
	"testing"

	"localcode/internal/events"
	"localcode/internal/tools"
)

// A background task that ran inside an archived conversation is not
// replayed either.
//
// A task session is visible:false and carries no archived flag of its
// own, so it used to be rehydrated whatever its parent was. That was
// merely wasteful while every log was read at startup anyway; now that a
// shelved log stays on disk until something asks for it, replaying one
// is what would ask.
func TestATaskInsideAnArchivedConversationIsNotReplayed(t *testing.T) {
	loop, _ := scriptedLoop(t, &scriptedProvider{}, tools.NewRegistry(nil))

	// A conversation with a task in it, and a live one beside it.
	for _, s := range []struct{ id, parent string }{
		{"s-shelf", ""},
		{"s-shelf-task", "s-shelf"},
		{"s-live", ""},
		{"s-live-task", "s-live"},
	} {
		visible := s.parent == ""
		if _, err := loop.Store.CreateSession(s.id, s.parent, "general-purpose", visible); err != nil {
			t.Fatalf("CreateSession %s: %v", s.id, err)
		}
		if _, err := loop.Store.Append(s.id, events.TypeUserMessage, map[string]any{"text": "do it"}); err != nil {
			t.Fatalf("Append %s: %v", s.id, err)
		}
	}
	if _, err := loop.Store.Archive("s-shelf"); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	loop.setHistory("s-shelf", nil)
	loop.setHistory("s-shelf-task", nil)
	loop.setHistory("s-live", nil)
	loop.setHistory("s-live-task", nil)
	loop.RehydrateAll()

	for _, id := range []string{"s-shelf", "s-shelf-task"} {
		if n := len(loop.history(id)); n != 0 {
			t.Errorf("%s was replayed at startup (%d messages) — it is on the shelf", id, n)
		}
	}
	for _, id := range []string{"s-live", "s-live-task"} {
		if n := len(loop.history(id)); n == 0 {
			t.Errorf("%s was not replayed — a live conversation lost its memory", id)
		}
	}
}
