package main

import (
	"os"
	"path/filepath"
	"testing"

	"localcode/internal/commands"
)

// The resolver is only useful if the loaders actually use it. A skill and
// a command written for Claude Code, in a home that has ~/.claude, must
// reach localcode without being copied anywhere.
func TestSkillsAndCommandsComeFromTheChosenRoot(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".claude", "skills", "deploy", "SKILL.md"),
		"---\nname: deploy\ndescription: ship it\n---\n\nrun the deploy script\n")
	write(t, filepath.Join(home, ".claude", "commands", "standup.md"),
		"---\ndescription: daily standup\n---\n\nWhat did I do yesterday?\n")
	// Present, and never read: the chain stops at the first root.
	write(t, filepath.Join(home, ".localcode", "skills", "ignored", "SKILL.md"),
		"---\nname: ignored\ndescription: not this one\n---\n\nno\n")
	write(t, filepath.Join(home, ".localcode", "commands", "ignored.md"), "no\n")

	e := env{home: home, cwd: t.TempDir()}

	list, err := loadSkills(e)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "deploy" {
		t.Errorf("skills = %+v, want only the one under ~/.claude", list)
	}

	_, global := assetsFor(e)
	cmdList, err := commands.LoadAll(filepath.Join(e.cwd, ".localcode", "commands"), global.Commands)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmdList) != 1 || cmdList[0].Name != "standup" {
		t.Errorf("commands = %+v, want only the one under ~/.claude", cmdList)
	}
}

// The chain runs under the project too, and the two are resolved
// independently: a repo that keeps its skills in .claude and a home that
// keeps its own in .localcode is an ordinary arrangement, not a clash.
func TestTheProjectRunsItsOwnChain(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, ".claude", "skills", "repo-lint", "SKILL.md"),
		"---\nname: repo-lint\ndescription: this repo's linter\n---\n\nrun make lint\n")
	write(t, filepath.Join(cwd, ".localcode", "skills", "shadowed", "SKILL.md"),
		"---\nname: shadowed\ndescription: never reached\n---\n\nno\n")
	write(t, filepath.Join(home, ".localcode", "skills", "global-one", "SKILL.md"),
		"---\nname: global-one\ndescription: the home skill\n---\n\nyes\n")

	e := env{home: home, cwd: cwd}
	project, global := assetsFor(e)
	if project.Chosen != ".claude" || global.Chosen != ".localcode" {
		t.Fatalf("project chose %q, home chose %q", project.Chosen, global.Chosen)
	}

	list, err := loadSkills(e)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, sk := range list {
		names[sk.Name] = true
	}
	if !names["repo-lint"] || !names["global-one"] {
		t.Errorf("skills = %v, want the project's .claude one and the home's .localcode one", names)
	}
	if names["shadowed"] {
		t.Errorf("skills = %v, want nothing from a project root the chain never reaches", names)
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
