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

// The first root that exists wins whole, and reading a config written for
// opencode makes a repository carrying both .opencode and .localcode an
// ordinary arrangement rather than an odd one. So the case this reports is
// about to become common: somebody runs opencode once in their own
// repository, and from then on localcode reads .opencode and their
// .localcode/skills is unread with nothing saying so.
func TestARootWithAssetsSaysSoWhenItLoses(t *testing.T) {
	dir := t.TempDir()
	mk(t, dir, ".opencode")
	mk(t, dir, ".localcode", "skills")

	got := At(dir)
	if got.Chosen != ".opencode" {
		t.Fatalf("chosen = %q, want .opencode — first existing root wins", got.Chosen)
	}
	if len(got.Shadowed) != 1 || got.Shadowed[0] != ".localcode" {
		t.Errorf("shadowed = %v, want [.localcode], which has skills in it and is not read", got.Shadowed)
	}
}

// Existing is not enough. A .localcode holding only a config.json loses
// nothing by losing, and a line about it on every start is one people
// learn to skip past.
func TestARootWithNothingInItIsNotWorthAWord(t *testing.T) {
	dir := t.TempDir()
	mk(t, dir, ".opencode", "skills")
	mk(t, dir, ".localcode")

	if got := At(dir); len(got.Shadowed) != 0 {
		t.Errorf("shadowed = %v, want none: the loser had no skills and no commands", got.Shadowed)
	}
}

// opencode names its commands directory in the singular, and that counts.
func TestTheSingularCommandDirectoryCounts(t *testing.T) {
	dir := t.TempDir()
	mk(t, dir, ".claude", "skills")
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
	mk(t, dir, ".localcode", "skills")
	if got := At(dir); len(got.Shadowed) != 0 {
		t.Errorf("shadowed = %v, want none", got.Shadowed)
	}
	empty := t.TempDir()
	if got := At(empty); got.Chosen != ".localcode" || len(got.Shadowed) != 0 {
		t.Errorf("a directory with no root at all: chosen=%q shadowed=%v", got.Chosen, got.Shadowed)
	}
}
