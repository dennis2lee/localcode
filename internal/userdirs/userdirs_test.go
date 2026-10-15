package userdirs

import (
	"os"
	"path/filepath"
	"strings"
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

// The first root that exists answers for global rules and the rest are
// never looked at. Somebody with all three installed gets one AGENTS.md,
// not a merge of three. Skills and commands are the other rule and are
// tested below.
func TestTheFirstRootThatExistsWinsOutright(t *testing.T) {
	home := t.TempDir()
	mkdirs(t, home, ".claude", ".opencode", ".localcode")

	got := At(home)
	if got.Chosen != ".claude" || got.Path != filepath.Join(home, ".claude") {
		t.Fatalf("At = %+v, want .claude", got)
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
			if got := At(home).Chosen; got != tc.chosen {
				t.Errorf("chose %q, want %q", got, tc.chosen)
			}
		})
	}
}

// A home with nothing in it yet answers with localcode's own root, so a
// first run points where the documentation says to put a first file.
func TestAFreshHomeAnswersWithLocalcode(t *testing.T) {
	home := t.TempDir()
	got := At(home)
	if got.Chosen != ".localcode" || got.Path != filepath.Join(home, ".localcode") {
		t.Errorf("At on an empty home = %+v", got)
	}
}

// Commands are read from the six roots skills are, in the same order: the
// project's .localcode, .claude, .opencode, then the home's. The loader keeps
// the first command of a name, so this order is the precedence.
func TestCommandDirsListEveryRootProjectFirst(t *testing.T) {
	got := CommandDirs(filepath.Join("/repo"), filepath.Join("/home", "u"))
	want := []string{
		filepath.Join("/repo", ".localcode", "commands"),
		filepath.Join("/repo", ".claude", "commands"),
		filepath.Join("/repo", ".opencode", "commands"),
		filepath.Join("/home", "u", ".localcode", "commands"),
		filepath.Join("/home", "u", ".claude", "commands"),
		filepath.Join("/home", "u", ".opencode", "commands"),
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("CommandDirs = %v, want %v", got, want)
	}
	if home := t.TempDir(); len(CommandDirs(home, home)) != len(MergedOrder) {
		t.Errorf("the project as the home named %d directories, want %d", len(CommandDirs(home, home)), len(MergedOrder))
	}
}

// opencode names its command directory in the singular. A root that has only
// that is read from it; one that somehow has both is read from the plural,
// the documented name.
func TestOpencodeCommandDirectoryIsAccepted(t *testing.T) {
	home := t.TempDir()
	mkdirs(t, home, ".opencode/command", ".claude/command", ".claude/commands")

	got := CommandDirs(home, home)
	if got[1] != filepath.Join(home, ".claude", "commands") {
		t.Errorf("a root with both: %q, want the plural", got[1])
	}
	if got[2] != filepath.Join(home, ".opencode", "command") {
		t.Errorf("a root with only the singular: %q, want the singular", got[2])
	}
}

// Every path this returns is built with filepath.Join, so it is the
// platform's own separator rather than a slash written into a string.
// The Windows job runs this package whole for exactly this claim.
func TestPathsUseThePlatformSeparator(t *testing.T) {
	dir := t.TempDir()
	mkdirs(t, dir, ".claude/skills", ".claude/commands")

	if got := At(dir).Path; got != filepath.Clean(got) {
		t.Errorf("At gave %q, want the cleaned platform path", got)
	}
	for _, d := range append(SkillDirs(dir, dir), CommandDirs(dir, dir)...) {
		if want := filepath.Clean(d); d != want {
			t.Errorf("a directory came back as %q, want the cleaned platform path %q", d, want)
		}
	}
}

// Skills are the other rule: every root is read, the project's before the
// home's, and inside each .localcode, then .claude, then .opencode. The
// loader keeps the first skill of a name, so this order is the precedence.
// localcode's own comes first for skills while it comes last for commands:
// a skill written in .localcode was written for this program.
func TestSkillDirsListEveryRootProjectFirst(t *testing.T) {
	got := SkillDirs(filepath.Join("/repo"), filepath.Join("/home", "u"))
	want := []string{
		filepath.Join("/repo", ".localcode", "skills"),
		filepath.Join("/repo", ".claude", "skills"),
		filepath.Join("/repo", ".opencode", "skills"),
		filepath.Join("/home", "u", ".localcode", "skills"),
		filepath.Join("/home", "u", ".claude", "skills"),
		filepath.Join("/home", "u", ".opencode", "skills"),
	}
	if len(got) != len(want) {
		t.Fatalf("SkillDirs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("directory %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// Naming a directory that does not exist is the point: the loader skips it,
// and one created after startup is found on the next reload because this is
// asked again then, with no root having been chosen in between.
func TestSkillDirsNameDirectoriesThatDoNotExistYet(t *testing.T) {
	home := t.TempDir()
	for _, d := range SkillDirs(home, home) {
		if _, err := os.Stat(d); err == nil {
			t.Fatalf("%s exists in an empty home", d)
		}
	}
	mkdirs(t, home, ".opencode/skills")
	var seen bool
	for _, d := range SkillDirs(home, home) {
		if d == filepath.Join(home, ".opencode", "skills") {
			seen = true
		}
	}
	if !seen {
		t.Error("a skills directory made after the first call is not in the list")
	}
}

// Running localcode in a home directory makes the project and the home one
// place. Each skills directory is named once, so neither the startup line
// nor the reload report lists the same three paths twice.
func TestSkillDirsNamesEachDirectoryOnceWhenTheProjectIsTheHome(t *testing.T) {
	home := t.TempDir()
	got := SkillDirs(home, home)
	if len(got) != len(MergedOrder) {
		t.Fatalf("SkillDirs(home, home) = %v, want %d entries", got, len(MergedOrder))
	}
	seen := map[string]bool{}
	for _, d := range got {
		if seen[d] {
			t.Errorf("%s is named twice", d)
		}
		seen[d] = true
	}
	// Spelled differently, still one place.
	if again := SkillDirs(home+string(filepath.Separator), home); len(again) != len(MergedOrder) {
		t.Errorf("a trailing separator made it two places: %v", again)
	}
}

// The same directory under another name is still one place. A project that
// is a link to the home directory is the home directory, and the file
// system is what says so. Spelling is what the first answer was.
func TestSkillDirsNamesTheHomeOnceWhenTheProjectIsALinkToIt(t *testing.T) {
	home := t.TempDir()
	link := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(home, link); err != nil {
		t.Skipf("no symbolic link can be made here: %v", err)
	}
	for _, c := range []struct{ name, project, home string }{
		{"the project is the link", link, home},
		{"the home is the link", home, link},
	} {
		if got := SkillDirs(c.project, c.home); len(got) != len(MergedOrder) {
			t.Errorf("%s: %d directories named, want %d: %v", c.name, len(got), len(MergedOrder), got)
		}
		if got := CommandDirs(c.project, c.home); len(got) != len(MergedOrder) {
			t.Errorf("%s: %d commands directories named, want %d: %v", c.name, len(got), len(MergedOrder), got)
		}
	}
}

func TestSameDirectory(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	missing := filepath.Join(dir, "not-there")
	file := filepath.Join(dir, "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"one spelling", dir, dir, true},
		{"a trailing separator", dir + string(filepath.Separator), dir, true},
		{"a dot segment", filepath.Join(dir, "."), dir, true},
		{"two directories", dir, other, false},
		{"a directory and its parent", dir, filepath.Dir(dir), false},
		// Nothing to examine, so only the spelling can say.
		{"one spelling of a missing path", missing, missing, true},
		{"two missing paths", missing, filepath.Join(dir, "also-not-there"), false},
		{"a missing path and a directory", missing, dir, false},
		{"a file and its directory", file, dir, false},
	}
	for _, c := range cases {
		if got := SameDirectory(c.a, c.b); got != c.want {
			t.Errorf("%s: SameDirectory(%q, %q) = %v, want %v", c.name, c.a, c.b, got, c.want)
		}
	}

	// The same directory under a link.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err == nil {
		if !SameDirectory(link, dir) || !SameDirectory(dir, link) {
			t.Errorf("a link to a directory is not the same directory as it")
		}
	}

	// The same directory spelled in another case, on a volume that ignores
	// case: macOS by default, and Windows. Where the other spelling names
	// nothing, the volume tells them apart and there is nothing to check.
	upper := strings.ToUpper(dir)
	if upper != dir {
		if _, err := os.Stat(upper); err == nil && !SameDirectory(upper, dir) {
			t.Errorf("%q and %q are one directory on this volume, and SameDirectory says they are not", upper, dir)
		}
	}
}
