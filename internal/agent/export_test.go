package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/events"
)

// A conversation as a file.
//
// There was no way to get one out: it lived in the event log and on two
// screens, and anybody who wanted it somewhere else took a screenshot or
// scrolled and copied.

func TestExportWritesTheConversationToAFile(t *testing.T) {
	loop, sid, bodies := effortLoop(t, "")
	dir := t.TempDir()
	if _, err := loop.Store.SetWorkspace(sid, dir); err != nil {
		t.Fatal(err)
	}

	loop.Store.Append(sid, events.TypeUserMessage, map[string]any{"text": "what does this do"})
	loop.Store.Append(sid, events.TypeToolStart, map[string]any{"tool_use_id": "t1", "name": "read_file"})
	loop.Store.Append(sid, events.TypeToolEnd, map[string]any{"tool_use_id": "t1", "content": "package main"})
	loop.Store.Append(sid, events.TypeMessagePartEnd, map[string]any{"text": "It is the entry point."})

	if err := loop.SendMessage(context.Background(), sid, "boy", "/export"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	// Written to a file, not printed: printing it would put a copy of the
	// whole conversation inside the conversation, and the next request
	// would carry it.
	if n := len(bodies()); n != 0 {
		t.Fatalf("/export reached the model (%d requests)", n)
	}

	entries, err := filepath.Glob(filepath.Join(dir, "session-*.md"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("glob = %v (%v), want one exported file", entries, err)
	}
	body, err := os.ReadFile(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{"what does this do", "read_file", "package main", "It is the entry point."} {
		if !strings.Contains(got, want) {
			t.Errorf("the file does not contain %q:\n%s", want, got)
		}
	}
	// 0600, like the session logs: this file is the conversation in one
	// piece, in a directory somebody chose, and 0644 would leave a
	// readable copy of everything in a project on a shared machine.
	info, err := os.Stat(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("the exported file is %04o, want nothing for group or other", perm)
	}
}

// An undone turn is not in the file, because it is not in the
// conversation — the same reading of the log the model gets.
func TestExportLeavesOutAnUndoneTurn(t *testing.T) {
	loop, sid, _ := effortLoop(t, "")
	dir := t.TempDir()
	if _, err := loop.Store.SetWorkspace(sid, dir); err != nil {
		t.Fatal(err)
	}

	loop.Store.Append(sid, events.TypeUserMessage, map[string]any{"text": "the kept turn"})
	loop.Store.Append(sid, events.TypeMessagePartEnd, map[string]any{"text": "kept answer"})
	undone, err := loop.Store.Append(sid, events.TypeUserMessage, map[string]any{"text": "the undone turn"})
	if err != nil {
		t.Fatal(err)
	}
	loop.Store.Append(sid, events.TypeMessagePartEnd, map[string]any{"text": "undone answer"})
	loop.Store.Append(sid, events.TypeRewound, map[string]any{"from_seq": undone.Seq})

	if err := loop.SendMessage(context.Background(), sid, "boy", "/export"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	entries, _ := filepath.Glob(filepath.Join(dir, "session-*.md"))
	if len(entries) != 1 {
		t.Fatalf("no exported file: %v", entries)
	}
	body, _ := os.ReadFile(entries[0])
	got := string(body)
	if !strings.Contains(got, "the kept turn") {
		t.Error("the kept turn is missing")
	}
	if strings.Contains(got, "undone answer") {
		t.Errorf("an undone turn is in the file:\n%s", got)
	}
}

// A long tool result is cut, and the file says where rather than leaving
// it to be discovered at the bottom.
func TestExportCutsALongToolResultAndSaysSo(t *testing.T) {
	long := strings.Repeat("x", exportToolLimit+500)
	out := cut(long)
	if len(out) >= len(long) {
		t.Error("a long result was not cut")
	}
	if !strings.Contains(out, "500 more characters") {
		t.Errorf("the cut is not named: %q", out[len(out)-80:])
	}
	// And a short one is untouched.
	if got := cut("short"); got != "short" {
		t.Errorf("cut(%q) = %q", "short", got)
	}
}

// A path names the file; a directory means "in there, under the default
// name", which is what somebody means by "/export ~/Desktop".
func TestExportTakesAPathOrADirectory(t *testing.T) {
	dir := t.TempDir()

	got, err := ResolveExportTarget("notes.md", dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dir, "notes.md") {
		t.Errorf("a relative path resolved to %q", got)
	}

	sub := filepath.Join(dir, "out")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveExportTarget(sub, dir); err != nil || got != filepath.Join(sub, "session.md") {
		t.Errorf("a directory resolved to %q (%v)", got, err)
	}

	// A parent that is not there is refused rather than failing at the
	// write, so the message names the directory rather than the file.
	if _, err := ResolveExportTarget(filepath.Join(dir, "nope", "x.md"), dir); err == nil {
		t.Error("a path under a directory that does not exist was accepted")
	}
}
