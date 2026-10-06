package userdirs

import (
	"os"
	"path/filepath"
	"testing"
)

func mk(t *testing.T, parts ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(parts...), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The first root that exists wins whole for commands, and a repository
// carrying both .opencode and .localcode is an ordinary arrangement. So the
// case this reports is not rare: somebody runs opencode once in their own
// repository, and from then on localcode reads .opencode's commands and
// their .localcode/commands is unread with nothing saying so.
func TestARootWithCommandsSaysSoWhenItLoses(t *testing.T) {
	dir := t.TempDir()
	mk(t, dir, ".opencode")
	mk(t, dir, ".localcode", "commands")

	got := At(dir)
	if got.Chosen != ".opencode" {
		t.Fatalf("chosen = %q, want .opencode — first existing root wins", got.Chosen)
	}
	if len(got.Shadowed) != 1 || got.Shadowed[0] != ".localcode" {
		t.Errorf("shadowed = %v, want [.localcode], which has commands in it and is not read", got.Shadowed)
	}
}

// Skills are never lost to a root that comes first, since every root is
// read, so a skills directory is not a reason to say anything.
func TestARootWithOnlySkillsLosesNothing(t *testing.T) {
	dir := t.TempDir()
	mk(t, dir, ".opencode", "commands")
	mk(t, dir, ".localcode", "skills")

	if got := At(dir); len(got.Shadowed) != 0 {
		t.Errorf("shadowed = %v, want none: the loser had skills only, and those are read", got.Shadowed)
	}
}

// Existing is not enough. A .localcode holding only a config.json loses
// nothing by losing, and a line about it on every start is one people
// learn to skip past.
func TestARootWithNothingInItIsNotWorthAWord(t *testing.T) {
	dir := t.TempDir()
	mk(t, dir, ".opencode", "commands")
	mk(t, dir, ".localcode")

	if got := At(dir); len(got.Shadowed) != 0 {
		t.Errorf("shadowed = %v, want none: the loser had no commands", got.Shadowed)
	}
}

// opencode names its commands directory in the singular, and that counts.
func TestTheSingularCommandDirectoryCounts(t *testing.T) {
	dir := t.TempDir()
	mk(t, dir, ".claude", "commands")
	mk(t, dir, ".opencode", "command")

	got := At(dir)
	if got.Chosen != ".claude" {
		t.Fatalf("chosen = %q, want .claude", got.Chosen)
	}
	if len(got.Shadowed) != 1 || got.Shadowed[0] != ".opencode" {
		t.Errorf("shadowed = %v, want [.opencode], whose command/ is not read", got.Shadowed)
	}
}

// The ordinary case, which is nearly every machine: one root, nothing
// lost, nothing said.
func TestOneRootShadowsNothing(t *testing.T) {
	dir := t.TempDir()
	mk(t, dir, ".localcode", "commands")
	if got := At(dir); len(got.Shadowed) != 0 {
		t.Errorf("shadowed = %v, want none", got.Shadowed)
	}
	empty := t.TempDir()
	if got := At(empty); got.Chosen != ".localcode" || len(got.Shadowed) != 0 {
		t.Errorf("a directory with no root at all: chosen=%q shadowed=%v", got.Chosen, got.Shadowed)
	}
}
