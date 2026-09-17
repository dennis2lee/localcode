package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/config"
	"localcode/internal/events"
	"localcode/internal/provider"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// The parser decides on the first two characters after trimming space: a
// single "!" runs, a doubled one is an escape, anything else is not a
// shell turn at all.
func TestParseBang(t *testing.T) {
	cases := []struct {
		text    string
		command string
		ok      bool
	}{
		{"!ls", "ls", true},
		{"!  git status ", "git status", true},
		{"  !echo x", "echo x", true},
		{"!", "", true},
		{"!!hi", "", false},
		{"!!", "", false},
		{"hello", "", false},
		{"a ! b", "", false},
		{"/status", "", false},
	}
	for _, tc := range cases {
		if command, ok := parseBang(tc.text); command != tc.command || ok != tc.ok {
			t.Errorf("parseBang(%q) = (%q, %v), want (%q, %v)", tc.text, command, ok, tc.command, tc.ok)
		}
	}
}

// Only the doubled form escapes, and it leaves exactly one "!": the
// remainder is an ordinary message that begins with one.
func TestStripBangEscape(t *testing.T) {
	cases := []struct {
		text string
		rest string
		ok   bool
	}{
		{"!!hi", "!hi", true},
		{"!!", "!", true},
		{"  !!/status", "!/status", true},
		{"!hi", "", false},
		{"hi", "", false},
	}
	for _, tc := range cases {
		if rest, ok := stripBangEscape(tc.text); rest != tc.rest || ok != tc.ok {
			t.Errorf("stripBangEscape(%q) = (%q, %v), want (%q, %v)", tc.text, rest, ok, tc.rest, tc.ok)
		}
	}
}

// bangLoop builds a loop whose model URL is unreachable, so any turn
// that touches the model fails loudly: a "!" turn must never do that.
func bangLoop(t *testing.T, projectDir string) (*Loop, *session.Store, string) {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	const sid = "bang-s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}
	cfg := &config.Config{
		Providers:      map[string]config.ProviderConfig{"local": {Type: config.ProviderOpenAICompat, BaseURL: "http://127.0.0.1:1/unreachable"}},
		Profiles:       map[string]config.Profile{"balanced": {Provider: "local", Model: "test-model"}},
		Agents:         map[string]config.AgentConfig{"general-purpose": {Profile: "balanced"}},
		DefaultProfile: "balanced",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid test config: %v", err)
	}
	loop := New(store, tools.NewRegistry(nil), map[string]provider.Provider{
		"local": provider.NewOpenAICompat("http://127.0.0.1:1/unreachable", ""),
	}, cfg)
	loop.SetProjectDir(projectDir)
	return loop, store, sid
}

// A "!" turn runs the command and records both halves, with no model
// call: the unreachable URL means reaching one returns an error, so a
// nil return proves the model was never asked.
func TestBangRunsTheCommandWithNoModelCall(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("workspace-marker-771\n"), 0o600); err != nil {
		t.Fatalf("write f.txt: %v", err)
	}
	loop, store, sid := bangLoop(t, dir)

	// Relative on purpose: the command runs in the session's workspace.
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "!cat f.txt"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	evs, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var sawUser, sawShell bool
	for _, ev := range evs {
		if ev.Type == events.TypeUserMessage {
			if txt, _ := ev.Data["text"].(string); txt == "!cat f.txt" {
				sawUser = true
				if local, _ := ev.Data["local"].(bool); local {
					t.Errorf("bang user message is local, so the model would never see the person ran it")
				}
			}
		}
		if ev.Type == events.TypeMessagePartEnd {
			if cmd, _ := ev.Data["shell_command"].(string); cmd == "cat f.txt" {
				sawShell = true
				if txt, _ := ev.Data["text"].(string); !strings.Contains(txt, "workspace-marker-771") {
					t.Errorf("shell output missing the file content:\n%s", txt)
				}
			}
		}
	}
	if !sawUser || !sawShell {
		t.Errorf("sawUser=%v sawShell=%v, want both halves of the turn recorded", sawUser, sawShell)
	}
	// The model sees the output on the next turn: the in-memory history
	// closes with the person's line and the command's answer, the shape
	// appendDelegatedTurn records for a turn the model did not run.
	hist := loop.history(sid)
	if len(hist) < 2 || hist[len(hist)-2].Role != provider.RoleUser || hist[len(hist)-1].Role != provider.RoleAssistant {
		t.Fatalf("history does not close with a user+assistant pair: %v", hist)
	}
	if got := hist[len(hist)-1].Content[0].Text; !strings.Contains(got, "workspace-marker-771") {
		t.Errorf("history answer missing the command output:\n%s", got)
	}
}

// "!" on its own runs nothing: a local usage error answers it.
func TestBangAloneIsAUsageError(t *testing.T) {
	loop, store, sid := bangLoop(t, t.TempDir())

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "!"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	evs, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var sawErr bool
	for _, ev := range evs {
		if ev.Type == events.TypeError {
			if msg, _ := ev.Data["error"].(string); strings.Contains(msg, "say what to run") {
				sawErr = true
			}
		}
		if ev.Type == events.TypeMessagePartEnd {
			if _, ok := ev.Data["shell_command"]; ok {
				t.Errorf("bare ! produced command output")
			}
		}
	}
	if !sawErr {
		t.Errorf("bare ! produced no usage error")
	}
}

// A delegated task starting with "!" is work, not a command: the shell
// never runs. The model URL is unreachable, so the turn itself fails —
// the assertion is what did not happen first. If the guard broke, the
// command would run before any model contact and this would be a nil
// error with the marker on disk.
func TestBangSkippedForADelegatedTask(t *testing.T) {
	loop, store, sid := bangLoop(t, t.TempDir())

	marker := filepath.Join(t.TempDir(), "delegated-marker.txt")
	text := "!touch " + marker
	ctx := withDelegatedTask(context.Background(), "build", text)
	if err := loop.SendMessage(ctx, sid, "general-purpose", text); err == nil {
		t.Fatalf("SendMessage with an unreachable model returned nil, want the model failure")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("delegated ! prompt ran a command: %s exists", marker)
	}
	evs, err := store.Events(sid, 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	for _, ev := range evs {
		if ev.Type == events.TypeMessagePartEnd {
			if _, ok := ev.Data["shell_command"]; ok {
				t.Errorf("delegated ! prompt produced command output")
			}
		}
	}
}
