package agent

import (
	"context"
	"strings"
	"testing"

	"localcode/internal/events"
)

func TestConfigCommandNoArgsShowsSummary(t *testing.T) {
	loop, store := newCustomCommandTestLoop(t, "", nil)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/config"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	text := lastMessagePartEnd(t, store, sid)
	if !strings.Contains(text, "auto_compact:") || !strings.Contains(text, "show_tps:") {
		t.Errorf("text = %q, want it to mention both settings", text)
	}
}

func TestConfigCommandTogglesAutoCompact(t *testing.T) {
	loop, store := newCustomCommandTestLoop(t, "", nil)
	loop.SetAutoCompactEnabled(true)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/config auto_compact off"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if loop.AutoCompactEnabled() {
		t.Error("expected AutoCompactEnabled() to be false after \"/config auto_compact off\"")
	}
	text := lastMessagePartEnd(t, store, sid)
	if !strings.Contains(text, "auto_compact: off") {
		t.Errorf("text = %q, want confirmation of the new setting", text)
	}
}

func TestConfigCommandTogglesShowTPS(t *testing.T) {
	loop, store := newCustomCommandTestLoop(t, "", nil)
	loop.SetShowTPS(true)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/config show_tps off"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	if loop.ShowTPS() {
		t.Error("expected ShowTPS() to be false after \"/config show_tps off\"")
	}
}

func TestConfigCommandEmitsConfigChangedEvent(t *testing.T) {
	loop, store := newCustomCommandTestLoop(t, "", nil)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/config auto_compact off"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	all, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var sawConfigChanged bool
	for _, ev := range all {
		if ev.Type == events.TypeConfigChanged {
			sawConfigChanged = true
			if ev.Data["auto_compact_enabled"] != false {
				t.Errorf("auto_compact_enabled = %v, want false", ev.Data["auto_compact_enabled"])
			}
		}
	}
	if !sawConfigChanged {
		t.Error("expected a config.changed event")
	}
}

func TestConfigCommandInvalidUsage(t *testing.T) {
	loop, store := newCustomCommandTestLoop(t, "", nil)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/config nonsense"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	text := lastMessagePartEnd(t, store, sid)
	if !strings.Contains(text, "usage") {
		t.Errorf("text = %q, want a usage message for invalid input", text)
	}
}

func TestConfigCommandUnknownSettingName(t *testing.T) {
	loop, store := newCustomCommandTestLoop(t, "", nil)
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/config bogus_setting on"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	text := lastMessagePartEnd(t, store, sid)
	if !strings.Contains(text, "bogus_setting") {
		t.Errorf("text = %q, want it to mention the unknown setting name", text)
	}
}
