package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/agent"
	"localcode/internal/userdirs"
)

func skillFile(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\nbody of " + name + "\n"
}

// The resolver is only useful if the loaders actually use it. A skill or a
// command written for Claude Code, one written for opencode and one of
// localcode's own, all in the one home, must all reach localcode without
// being copied anywhere. Commands used to come from the first root alone.
func TestSkillsAndCommandsComeFromEveryRoot(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".claude", "skills", "deploy", "SKILL.md"), skillFile("deploy", "ship it"))
	write(t, filepath.Join(home, ".opencode", "skills", "review", "SKILL.md"), skillFile("review", "read a diff"))
	write(t, filepath.Join(home, ".localcode", "skills", "own", "SKILL.md"), skillFile("own", "a localcode one"))
	write(t, filepath.Join(home, ".claude", "commands", "standup.md"), commandFile("daily standup"))
	// opencode's own directory name, in the singular.
	write(t, filepath.Join(home, ".opencode", "command", "triage.md"), commandFile("sort the issues"))
	write(t, filepath.Join(home, ".localcode", "commands", "mine.md"), commandFile("a localcode one"))

	cwd := t.TempDir()
	list, err := loadSkills(cwd, home)
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

	cmdList, err := loadCommands(cwd, home)
	if err != nil {
		t.Fatal(err)
	}
	got = map[string]bool{}
	for _, c := range cmdList {
		got[c.Name] = true
	}
	for _, want := range []string{"standup", "triage", "mine"} {
		if !got[want] {
			t.Errorf("commands = %v, want %q too: every root is read", got, want)
		}
	}
}

// The roots run under the project too, and every root of both places is
// read: a repo that keeps its own in .claude and a home that keeps its own
// in .localcode is an ordinary arrangement, not a clash.
func TestTheProjectAndTheHomeEachContributeEveryRoot(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, ".claude", "skills", "repo-lint", "SKILL.md"), skillFile("repo-lint", "this repo's linter"))
	write(t, filepath.Join(cwd, ".localcode", "skills", "repo-own", "SKILL.md"), skillFile("repo-own", "the repo's localcode skill"))
	write(t, filepath.Join(home, ".localcode", "skills", "global-one", "SKILL.md"), skillFile("global-one", "the home skill"))
	write(t, filepath.Join(cwd, ".claude", "commands", "repo-cmd.md"), commandFile("the repo's"))
	write(t, filepath.Join(cwd, ".localcode", "commands", "repo-own-cmd.md"), commandFile("the repo's localcode one"))
	write(t, filepath.Join(home, ".opencode", "commands", "global-cmd.md"), commandFile("the home's"))

	list, err := loadSkills(cwd, home)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, sk := range list {
		names[sk.Name] = true
	}
	cmdList, err := loadCommands(cwd, home)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cmdList {
		names["/"+c.Name] = true
	}
	for _, want := range []string{"repo-lint", "repo-own", "global-one", "/repo-cmd", "/repo-own-cmd", "/global-cmd"} {
		if !names[want] {
			t.Errorf("loaded %v, want %q: nothing is left out for the root it is in", names, want)
		}
	}
}

// A name that several roots have is one command, fixed by the same seven
// directories in the same order as a skill: the project's .localcode, .claude
// and .opencode, then the home's, then ~/.config/opencode. Each is removed in
// turn from the top, and the next one down must be the only command of that
// name. Two of opencode's roots use the singular command/ that older opencode
// versions create, which counts the same.
func TestACommandNameResolvesThroughEveryRootInOrder(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	chain := []string{
		filepath.Join(cwd, ".localcode", "commands"),
		filepath.Join(cwd, ".claude", "commands"),
		filepath.Join(cwd, ".opencode", "command"),
		filepath.Join(home, ".localcode", "commands"),
		filepath.Join(home, ".claude", "commands"),
		filepath.Join(home, ".opencode", "command"),
		filepath.Join(home, ".config", "opencode", "commands"),
	}
	for i, dir := range chain {
		write(t, filepath.Join(dir, "ship.md"), commandFile(fmt.Sprintf("level %d", i+1)))
	}
	for i, dir := range chain {
		loop := &agent.Loop{}
		if _, err := reloadProjectAssets(loop, startupTestRegistry(t), cwd, home); err != nil {
			t.Fatal(err)
		}
		var found []string
		for _, c := range loop.Commands {
			if c.Name == "ship" {
				found = append(found, c.Description)
			}
		}
		if want := fmt.Sprintf("level %d", i+1); len(found) != 1 || found[0] != want {
			t.Fatalf("level %d: commands named ship = %v, want only %q from %s", i+1, found, want, dir)
		}
		if err := os.Remove(filepath.Join(dir, "ship.md")); err != nil {
			t.Fatal(err)
		}
	}
}

func commandFile(description string) string {
	return "---\ndescription: " + description + "\n---\n\nDo the thing.\n"
}

// A name that several roots have is one skill, and which one is fixed by seven
// directories in order: the project's .localcode, .claude and .opencode, then
// the home's in the same order, then opencode's own ~/.config/opencode. Each is removed in turn from the top, and the
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
		filepath.Join(home, ".config", "opencode"),
	}
	spelled := []string{"shared", "Shared", "SHARED", "shared", "sHared", "Shared", "shareD"}
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
	for _, dir := range append(userdirs.SkillDirs(cwd, home), userdirs.CommandDirs(cwd, home)...) {
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

	dirs := existingDirs(userdirs.SkillDirs(home, home))
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

// Startup says which commands directories it read when one is outside
// .localcode, as it does for skills, and says nothing about the default. A
// leftover where a commands directory should be costs that root its
// commands, with one line naming it once, and does not cost the start.
func TestStartupNamesTheCommandsItReadsAndSurvivesALeftover(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	write(t, filepath.Join(workspace, ".localcode", "commands", "mine.md"), commandFile("mine"))
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	if _, _, _, _, _, _, err := buildSystemPrompt(startupTestConfig(), startupTestRegistry(t), workspace, home); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "commands: reading") {
		t.Errorf("a line about the default .localcode commands: %q", logs.String())
	}

	theirs := filepath.Join(workspace, ".claude", "commands")
	write(t, filepath.Join(theirs, "theirs.md"), commandFile("theirs"))
	leftover := filepath.Join(home, ".opencode", "commands")
	write(t, leftover, "a file, not a directory\n")
	logs.Reset()
	_, _, _, _, cmdList, _, err := buildSystemPrompt(startupTestConfig(), startupTestRegistry(t), workspace, home)
	if err != nil {
		t.Fatalf("a file where a commands directory should be stopped startup: %v", err)
	}
	names := map[string]bool{}
	for _, c := range cmdList {
		names[c.Name] = true
	}
	if !names["mine"] || !names["theirs"] {
		t.Errorf("commands = %v, want mine and theirs", names)
	}
	out := logs.String()
	if !strings.Contains(out, "commands: reading") || !strings.Contains(out, theirs) {
		t.Errorf("startup did not name %s: %q", theirs, out)
	}
	if n := strings.Count(out, leftover); n != 1 || !strings.Contains(out, "cannot be read") {
		t.Errorf("the leftover is named %d times, want once in a line saying it cannot be read: %q", n, out)
	}
}
