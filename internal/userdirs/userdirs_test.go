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

// The first root that exists answers for commands and the rest are never
// looked at. Somebody with all three installed gets one of them, not a
// merge of three. Skills are the other rule and are tested below.
func TestTheFirstRootThatExistsWinsOutright(t *testing.T) {
	home := t.TempDir()
	mkdirs(t, home, ".claude/skills", ".opencode/skills", ".localcode/skills", ".localcode/commands")

	got := At(home)
	if got.Chosen != ".claude" {
		t.Fatalf("chose %q, want .claude", got.Chosen)
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
			if got := At(home).Chosen; got != tc.chosen {
				t.Errorf("chose %q, want %q", got, tc.chosen)
			}
		})
	}
}

// A winner with nothing in it still wins. The directory is named anyway,
// so a command added later is found without a restart of anything but the
// loader, and so the loader reports "none" against a path a person can
// read rather than against nothing.
func TestAnEmptyWinnerStillWins(t *testing.T) {
	home := t.TempDir()
	mkdirs(t, home, ".claude", ".localcode/commands")

	got := At(home)
	if got.Chosen != ".claude" {
		t.Fatalf("chose %q, want .claude even though it holds nothing", got.Chosen)
	}
	if _, err := os.Stat(got.Commands); err == nil {
		t.Errorf("%s exists; the test meant to describe a root with no commands", got.Commands)
	}
	if filepath.Dir(got.Commands) != filepath.Join(home, ".claude") {
		t.Errorf("%s is not under the winning root", got.Commands)
	}
}

// opencode names its command directory in the singular. A root chosen
// for its opencode commands that then looked for "commands" would find
// nothing at all.
func TestOpencodeCommandDirectoryIsAccepted(t *testing.T) {
	home := t.TempDir()
	mkdirs(t, home, ".opencode/command")

	if got := At(home).Commands; got != filepath.Join(home, ".opencode", "command") {
		t.Errorf("commands = %q, want the singular directory opencode creates", got)
	}
}

// "commands" wins over "command" where a root somehow has both, so the
// documented name is the one that answers.
func TestThePluralNameWinsWhenBothExist(t *testing.T) {
	home := t.TempDir()
	mkdirs(t, home, ".opencode/command", ".opencode/commands")

	if got := At(home).Commands; got != filepath.Join(home, ".opencode", "commands") {
		t.Errorf("commands = %q", got)
	}
}

// A home with nothing in it yet answers with localcode's own paths, so a
// first run points where the documentation says to put a first skill.
func TestAFreshHomeAnswersWithLocalcode(t *testing.T) {
	home := t.TempDir()
	got := At(home)
	if got.Chosen != ".localcode" || got.Commands != filepath.Join(home, ".localcode", "commands") {
		t.Errorf("At on an empty home = %+v", got)
	}
}

// The chain knows nothing about homes: a project directory resolves the
// same way, and the two answers are independent of each other.
func TestAProjectResolvesIndependentlyOfAHome(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	mkdirs(t, home, ".localcode/skills")
	mkdirs(t, project, ".claude/skills", ".localcode/skills")

	if got := At(home).Chosen; got != ".localcode" {
		t.Errorf("home chose %q", got)
	}
	if got := At(project); got.Chosen != ".claude" || got.Commands != filepath.Join(project, ".claude", "commands") {
		t.Errorf("project = %+v, want its own .claude", got)
	}
}

// Every path this returns is built with filepath.Join, so it is the
// platform's own separator rather than a slash written into a string.
// The Windows job runs this package whole for exactly this claim.
func TestPathsUseThePlatformSeparator(t *testing.T) {
	dir := t.TempDir()
	mkdirs(t, dir, ".claude/skills", ".claude/commands")

	got := At(dir)
	for name, path := range map[string]string{
		"Path":     got.Path,
		"Commands": got.Commands,
	} {
		if want := filepath.Clean(path); path != want {
			t.Errorf("%s = %q, want the cleaned platform path %q", name, path, want)
		}
	}
	if filepath.Base(filepath.Dir(got.Commands)) != ".claude" {
		t.Errorf("%s does not sit under the chosen root", got.Commands)
	}
	for _, d := range SkillDirs(dir, dir) {
		if want := filepath.Clean(d); d != want {
			t.Errorf("SkillDirs gave %q, want the cleaned platform path %q", d, want)
		}
	}
}

// Skills are the other rule: every root is read, the project's before the
// home's, and inside each .claude, then .opencode, then .localcode. The
// loader keeps the first skill of a name, so this order is the precedence.
func TestSkillDirsListEveryRootProjectFirst(t *testing.T) {
	got := SkillDirs(filepath.Join("/repo"), filepath.Join("/home", "u"))
	want := []string{
		filepath.Join("/repo", ".claude", "skills"),
		filepath.Join("/repo", ".opencode", "skills"),
		filepath.Join("/repo", ".localcode", "skills"),
		filepath.Join("/home", "u", ".claude", "skills"),
		filepath.Join("/home", "u", ".opencode", "skills"),
		filepath.Join("/home", "u", ".localcode", "skills"),
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
	if len(got) != len(Order) {
		t.Fatalf("SkillDirs(home, home) = %v, want %d entries", got, len(Order))
	}
	seen := map[string]bool{}
	for _, d := range got {
		if seen[d] {
			t.Errorf("%s is named twice", d)
		}
		seen[d] = true
	}
	// Spelled differently, still one place.
	if again := SkillDirs(home+string(filepath.Separator), home); len(again) != len(Order) {
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
		if got := SkillDirs(c.project, c.home); len(got) != len(Order) {
			t.Errorf("%s: %d directories named, want %d: %v", c.name, len(got), len(Order), got)
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
