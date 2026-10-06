package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/agent"
	"localcode/internal/commands"
	"localcode/internal/userdirs"
)

func skillFile(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\nbody of " + name + "\n"
}

// The resolver is only useful if the loaders actually use it. A skill
// written for Claude Code, one written for opencode and one of localcode's
// own, all in the one home, must all reach localcode without being copied
// anywhere. Commands are the other rule: the first root answers for them
// and the others are not read.
func TestSkillsComeFromEveryRootAndCommandsFromTheChosenOne(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".claude", "skills", "deploy", "SKILL.md"), skillFile("deploy", "ship it"))
	write(t, filepath.Join(home, ".opencode", "skills", "review", "SKILL.md"), skillFile("review", "read a diff"))
	write(t, filepath.Join(home, ".localcode", "skills", "own", "SKILL.md"), skillFile("own", "a localcode one"))
	write(t, filepath.Join(home, ".claude", "commands", "standup.md"),
		"---\ndescription: daily standup\n---\n\nWhat did I do yesterday?\n")
	// Present, and never read: the chain for commands stops at the first root.
	write(t, filepath.Join(home, ".localcode", "commands", "ignored.md"), "no\n")

	e := env{home: home, cwd: t.TempDir()}

	list, err := loadSkills(e.cwd, e.home)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, sk := range list {
		got[sk.Name] = true
	}
	for _, want := range []string{"deploy", "review", "own"} {
		if !got[want] {
			t.Errorf("skills = %v, want %q too: every root is read", got, want)
		}
	}

	_, global := assetsFor(e.cwd, e.home)
	cmdList, err := commands.LoadAll(filepath.Join(e.cwd, ".localcode", "commands"), global.Commands)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmdList) != 1 || cmdList[0].Name != "standup" {
		t.Errorf("commands = %+v, want only the one under ~/.claude", cmdList)
	}
}

// The roots run under the project too, and the two places are resolved
// independently: a repo that keeps its skills in .claude and a home that
// keeps its own in .localcode is an ordinary arrangement, not a clash.
func TestTheProjectAndTheHomeEachContributeEveryRoot(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, ".claude", "skills", "repo-lint", "SKILL.md"), skillFile("repo-lint", "this repo's linter"))
	write(t, filepath.Join(cwd, ".localcode", "skills", "repo-own", "SKILL.md"), skillFile("repo-own", "the repo's localcode skill"))
	write(t, filepath.Join(home, ".localcode", "skills", "global-one", "SKILL.md"), skillFile("global-one", "the home skill"))

	e := env{home: home, cwd: cwd}
	project, global := assetsFor(e.cwd, e.home)
	if project.Chosen != ".claude" || global.Chosen != ".localcode" {
		t.Fatalf("commands: project chose %q, home chose %q", project.Chosen, global.Chosen)
	}

	list, err := loadSkills(e.cwd, e.home)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, sk := range list {
		names[sk.Name] = true
	}
	for _, want := range []string{"repo-lint", "repo-own", "global-one"} {
		if !names[want] {
			t.Errorf("skills = %v, want %q: nothing is left out for being in a root that lost", names, want)
		}
	}
}

// A name that two roots both have is one skill. The project's wins over the
// home's, as it always has; inside one place .claude comes before .opencode
// before .localcode, which is the order that decided the first-root rule, so
// a skill that resolved to a file before still does.
func TestTheEarlierRootWinsAName(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	write(t, filepath.Join(home, ".claude", "skills", "shared", "SKILL.md"), skillFile("shared", "from home claude"))
	write(t, filepath.Join(home, ".opencode", "skills", "shared", "SKILL.md"), skillFile("shared", "from home opencode"))
	write(t, filepath.Join(home, ".localcode", "skills", "shared", "SKILL.md"), skillFile("shared", "from home localcode"))
	write(t, filepath.Join(home, ".opencode", "skills", "only-here", "SKILL.md"), skillFile("only-here", "home opencode"))
	write(t, filepath.Join(home, ".localcode", "skills", "only-here", "SKILL.md"), skillFile("only-here", "home localcode"))
	write(t, filepath.Join(cwd, ".localcode", "skills", "only-here", "SKILL.md"), skillFile("only-here", "project localcode"))

	list, err := loadSkills(cwd, home)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]string{}
	for _, sk := range list {
		if _, dup := byName[sk.Name]; dup {
			t.Errorf("skill %q was loaded twice", sk.Name)
		}
		byName[sk.Name] = sk.Description
	}
	if got := byName["shared"]; got != "from home claude" {
		t.Errorf("shared = %q, want the .claude one", got)
	}
	if got := byName["only-here"]; got != "project localcode" {
		t.Errorf("only-here = %q, want the project's over every home root", got)
	}
}

// "/reset-skills" is the same load as startup, asked again, and it reports
// where it looked. Every skills directory is named, whether or not it
// exists, because the list is the answer to "where do I put a skill".
func TestResetSkillsReadsEveryRootAndNamesThem(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	write(t, filepath.Join(home, ".claude", "skills", "from-claude", "SKILL.md"), skillFile("from-claude", "a"))
	write(t, filepath.Join(home, ".opencode", "skills", "from-opencode", "SKILL.md"), skillFile("from-opencode", "b"))
	write(t, filepath.Join(cwd, ".localcode", "skills", "from-project", "SKILL.md"), skillFile("from-project", "c"))

	loop := &agent.Loop{}
	report, err := reloadProjectAssets(loop, startupTestRegistry(t), cwd, home)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, sk := range loop.SkillList() {
		got[sk.Name] = true
	}
	for _, want := range []string{"from-claude", "from-opencode", "from-project"} {
		if !got[want] {
			t.Errorf("the reload left out %q: %v", want, got)
		}
	}
	for _, dir := range userdirs.SkillDirs(cwd, home) {
		if !strings.Contains(report, dir) {
			t.Errorf("the report does not name %s: %s", dir, report)
		}
	}
}

// Every root is read now, so a root that used to lose is read too. What
// is in it may be a leftover: a file where "skills" should be. That costs
// the root its skills and must not cost the daemon its start.
func TestALeftoverInARootThatUsedToLoseDoesNotStopStartup(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".claude", "skills", "fine", "SKILL.md"), skillFile("fine", "from the root that always won"))
	write(t, filepath.Join(home, ".opencode", "skills"), "a file, not a directory\n")

	list, err := loadSkills(t.TempDir(), home)
	if err != nil {
		t.Fatalf("a file where .opencode/skills should be stopped the load: %v", err)
	}
	if len(list) != 1 || list[0].Name != "fine" {
		t.Errorf("skills = %+v, want the one that can be read", list)
	}
}

// Run in the home directory, the project and the home are one place and each
// skills directory is named once, in the startup line and in the reload report.
func TestRunningInTheHomeNamesEachSkillsDirectoryOnce(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".claude", "skills", "a", "SKILL.md"), skillFile("a", "x"))

	dirs := skillDirsRead(home, home)
	if len(dirs) != 1 {
		t.Errorf("skillDirsRead = %v, want the one directory that exists, once", dirs)
	}
	report, err := reloadProjectAssets(&agent.Loop{}, startupTestRegistry(t), home, home)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range userdirs.SkillDirs(home, home) {
		if n := strings.Count(report, d); n != 1 {
			t.Errorf("%s appears %d times in the report: %s", d, n, report)
		}
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
