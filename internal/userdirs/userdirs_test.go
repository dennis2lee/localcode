package userdirs

import (
	"os"
	"path/filepath"
	"testing"
)

func mkdirs(t *testing.T, home string, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(filepath.Join(home, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// The first root that exists takes both kinds and the rest are never
// looked at. Somebody with all three installed gets one of them, not a
// merge of three.
func TestTheFirstRootThatExistsWinsOutright(t *testing.T) {
	home := t.TempDir()
	mkdirs(t, home, ".claude/skills", ".opencode/skills", ".localcode/skills", ".localcode/commands")

	got := Assets(home)
	if got.Chosen != ".claude" {
		t.Fatalf("chose %q, want .claude", got.Chosen)
	}
	if got.Skills != filepath.Join(home, ".claude", "skills") {
		t.Errorf("skills = %q", got.Skills)
	}
	if got.Commands != filepath.Join(home, ".claude", "commands") {
		t.Errorf("commands = %q, want the winning root's own, not a lower one's", got.Commands)
	}
}

// Second place is reached only when the first is absent, and third only
// when both are.
func TestTheChainFallsThroughInOrder(t *testing.T) {
	for _, tc := range []struct {
		name   string
		make   []string
		chosen string
	}{
		{"opencode when there is no claude", []string{".opencode", ".localcode"}, ".opencode"},
		{"localcode when it is the only one", []string{".localcode"}, ".localcode"},
		{"claude over opencode", []string{".claude", ".opencode"}, ".claude"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			mkdirs(t, home, tc.make...)
			if got := Assets(home).Chosen; got != tc.chosen {
				t.Errorf("chose %q, want %q", got, tc.chosen)
			}
		})
	}
}

// A winner with nothing in it still wins. The directories are named
// anyway, so a skill added later is found without a restart of anything
// but the loader, and so the loaders report "none" against a path a
// person can read rather than against nothing.
func TestAnEmptyWinnerStillWins(t *testing.T) {
	home := t.TempDir()
	mkdirs(t, home, ".claude", ".localcode/skills", ".localcode/commands")

	got := Assets(home)
	if got.Chosen != ".claude" {
		t.Fatalf("chose %q, want .claude even though it holds nothing", got.Chosen)
	}
	for _, dir := range []string{got.Skills, got.Commands} {
		if _, err := os.Stat(dir); err == nil {
			t.Errorf("%s exists; the test meant to describe a root with neither", dir)
		}
		if filepath.Dir(dir) != filepath.Join(home, ".claude") {
			t.Errorf("%s is not under the winning root", dir)
		}
	}
}

// opencode names its command directory in the singular. A root chosen
// for its opencode commands that then looked for "commands" would find
// nothing at all.
func TestOpencodeCommandDirectoryIsAccepted(t *testing.T) {
	home := t.TempDir()
	mkdirs(t, home, ".opencode/command")

	if got := Assets(home).Commands; got != filepath.Join(home, ".opencode", "command") {
		t.Errorf("commands = %q, want the singular directory opencode creates", got)
	}
}

// "commands" wins over "command" where a root somehow has both, so the
// documented name is the one that answers.
func TestThePluralNameWinsWhenBothExist(t *testing.T) {
	home := t.TempDir()
	mkdirs(t, home, ".opencode/command", ".opencode/commands")

	if got := Assets(home).Commands; got != filepath.Join(home, ".opencode", "commands") {
		t.Errorf("commands = %q", got)
	}
}

// A home with nothing in it yet answers with localcode's own paths, so a
// first run points where the documentation says to put a first skill.
func TestAFreshHomeAnswersWithLocalcode(t *testing.T) {
	home := t.TempDir()
	got := Assets(home)
	if got.Chosen != ".localcode" || got.Skills != filepath.Join(home, ".localcode", "skills") {
		t.Errorf("Assets on an empty home = %+v", got)
	}
}
