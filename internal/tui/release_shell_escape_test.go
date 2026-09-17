package tui

import (
	"strings"
	"testing"

	"localcode/internal/events"
)

// Command output the person ran must not draw as something the model
// said: a message.part.end carrying shell_command lands as a tool entry
// naming the command, while an ordinary model reply stays a model entry.
func TestShellOutputDrawsAsACommandResult(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypeUserMessage, Data: map[string]any{"text": "!git status"}})
	m.applyEvent(events.Event{Type: events.TypeMessagePartEnd, Data: map[string]any{
		"text": " M main.go", "shell_command": "git status",
	}})

	if len(m.transcript) != 2 {
		t.Fatalf("transcript holds %d entries, want 2 (the typed line and its output)", len(m.transcript))
	}
	if got := m.transcript[0]; got.kind != entryUser || got.text != "!git status" {
		t.Errorf("typed line draws as %+v, want a user entry holding exactly what was typed", got)
	}
	got := m.transcript[1]
	if got.kind != entryTool {
		t.Errorf("command output draws as kind %d, want a tool entry rather than model text", got.kind)
	}
	if !strings.Contains(got.text, "$ git status") || !strings.Contains(got.text, "M main.go") {
		t.Errorf("command output entry names neither the command nor its output:\n%s", got.text)
	}
}

// The neighbouring behaviour that must not change: a reply without the
// marker is still a model message, drawn as one.
func TestPlainModelReplyStillDrawsAsModelText(t *testing.T) {
	m := newTestModel()
	m.applyEvent(events.Event{Type: events.TypeMessagePartEnd, Data: map[string]any{"text": "hello there"}})

	if len(m.transcript) != 1 {
		t.Fatalf("transcript holds %d entries, want 1", len(m.transcript))
	}
	if got := m.transcript[0]; got.kind != entryModel || got.text != "hello there" {
		t.Errorf("plain reply draws as %+v, want a model entry with the reply text", got)
	}
}
