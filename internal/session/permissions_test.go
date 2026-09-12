package session

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Who can read a conversation.
//
// A session log is the conversation itself — every prompt, every reply,
// every tool result — and since compaction began recording its summary in
// full, a condensed copy of all of it in one event. They were written
// 0644 in a 0755 directory, which on a shared machine is every other
// account on it. The checkpoint blobs in the same package have been 0700
// since they were added.

func TestASessionLogIsNotReadableByOtherAccounts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits")
	}
	dir := filepath.Join(t.TempDir(), "sessions")
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	if _, err := store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append("s1", "message.user", map[string]any{"text": "something private"}); err != nil {
		t.Fatal(err)
	}

	if info, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	} else if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("the session directory is %04o, want nothing for group or other", perm)
	}
	for _, name := range []string{"s1.jsonl", "s1.meta.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s is %04o, want nothing for group or other", name, perm)
		}
	}
}

// A directory that already exists keeps the mode it was made with, so
// creating it tighter only ever helps a new install. Every conversation
// written before this is in the old one.
func TestOpeningAnOldStoreNarrowsWhatIsAlreadyThere(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits")
	}
	dir := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "old.jsonl")
	meta := filepath.Join(dir, "old.meta.json")
	for _, p := range []string{log, meta} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Something the store does not write is left alone: narrowing a file
	// this package did not create would be reaching past what it knows.
	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)

	for _, p := range []string{dir, log, meta} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s is still %04o after opening the store", filepath.Base(p), perm)
		}
	}
	if info, err := os.Stat(other); err == nil && info.Mode().Perm() != 0o644 {
		t.Errorf("a file the store does not write was narrowed to %04o", info.Mode().Perm())
	}
}

// Opening a store must not fail over permissions it could not change: a
// store that will not open is a worse answer than one whose modes are as
// they were.
func TestAStoreStillOpensWhenItCannotNarrow(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	if _, err := store.CreateSession("s1", "", "general-purpose", true); err != nil {
		t.Errorf("the store opened but cannot be used: %v", err)
	}
}
