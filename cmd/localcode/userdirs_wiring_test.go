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

	cmdList, err := commands.LoadAll(filepath.Join(e.cwd, ".localcode", "commands"), assetsFor(e).Commands)
	if err != nil {
		t.Fatal(err)
	}
	if len(cmdList) != 1 || cmdList[0].Name != "standup" {
		t.Errorf("commands = %+v, want only the one under ~/.claude", cmdList)
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
