package main

import (
	"context"
	"encoding/json"
	"fmt"
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

// A name that several roots have is one skill, and which one is fixed by six
// directories in order: the project's .localcode, .claude and .opencode, then
// the home's in the same order. Each is removed in turn from the top, and the
// next one down must be the only skill of that name, the one listed, and the
// body the model gets from the Skill tool. A skill typed as /shared is found
// in that same list, so being the only one of its name is what makes the
// person typing it and the model calling it get the same file. The name's
// case differs from level to level, and the tool is asked in a case none of
// them uses, because names that differ only in case are one name.
func TestASkillNameResolvesThroughEveryRootInOrder(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	chain := []string{
		filepath.Join(cwd, ".localcode"),
		filepath.Join(cwd, ".claude"),
		filepath.Join(cwd, ".opencode"),
		filepath.Join(home, ".localcode"),
		filepath.Join(home, ".claude"),
		filepath.Join(home, ".opencode"),
	}
	spelled := []string{"shared", "Shared", "SHARED", "shared", "sHared", "Shared"}
	for i, root := range chain {
		write(t, filepath.Join(root, "skills", "shared", "SKILL.md"),
			fmt.Sprintf("---\nname: %s\ndescription: level %d\n---\n\nbody from level %d\n", spelled[i], i+1, i+1))
	}
	for i, root := range chain {
		loop := &agent.Loop{}
		registry := startupTestRegistry(t)
		if _, err := reloadProjectAssets(loop, registry, cwd, home); err != nil {
			t.Fatal(err)
		}
		var found []string
		for _, sk := range loop.SkillList() {
			if strings.EqualFold(sk.Name, "shared") {
				found = append(found, sk.Path)
			}
		}
		wantPath := filepath.Join(root, "skills", "shared", "SKILL.md")
		if len(found) != 1 || found[0] != wantPath {
			t.Fatalf("level %d: skills named shared = %v, want only %s", i+1, found, wantPath)
		}
		res := registry.Call(context.Background(), "Skill", json.RawMessage(`{"name":"ShArEd"}`), "")
		if want := fmt.Sprintf("body from level %d", i+1); res.IsError || !strings.Contains(res.Content, want) {
			t.Errorf("level %d: the Skill tool returned %q, want %q", i+1, res.Content, want)
		}
		if err := os.RemoveAll(filepath.Join(root, "skills", "shared")); err != nil {
			t.Fatal(err)
		}
	}
}

// A different name in a lower root is not shadowed by anything: every root
// still contributes the skills only it has.
func TestALowerRootStillAddsItsOwnSkills(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, ".localcode", "skills", "shared", "SKILL.md"), skillFile("shared", "project localcode"))
	write(t, filepath.Join(home, ".opencode", "skills", "shared", "SKILL.md"), skillFile("shared", "home opencode"))
	write(t, filepath.Join(home, ".opencode", "skills", "only-here", "SKILL.md"), skillFile("only-here", "home opencode"))

	list, err := loadSkills(cwd, home)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]string{}
	for _, sk := range list {
		byName[sk.Name] = sk.Description
	}
	if len(list) != 2 || byName["shared"] != "project localcode" || byName["only-here"] != "home opencode" {
		t.Errorf("skills = %v, want shared from the project's .localcode and only-here from the home's .opencode", byName)
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
