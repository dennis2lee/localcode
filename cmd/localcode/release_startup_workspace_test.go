package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/agent"
	"localcode/internal/config"
	"localcode/internal/session"
	"localcode/internal/tools"
)

// startupTestConfig is a config with the disk-touching extras off: the
// tests below are about which project directory skills and commands load
// from, not about memory or providers.
func startupTestConfig() *config.Config {
	off := false
	return &config.Config{AutoMemoryEnabled: &off}
}

func startupTestRegistry(t *testing.T) *tools.Registry {
	t.Helper()
	store, err := session.NewStore("")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(store.Close)
	return tools.NewRegistry(agent.NewPermissionBroker(store).Func())
}

// The daemon starts in one directory and works in another. On the Windows
// desktop that is the install directory and the opened project. Startup
// must read the project it works in: a skill installed in the workspace
// loads without any manual step, and a skill in the start directory does
// not leak in. The two directories are inputs, never the process working
// directory, so this runs on every platform.
func TestStartupLoadsSkillsFromTheWorkspaceNotTheStartDirectory(t *testing.T) {
	startDir := t.TempDir()
	write(t, filepath.Join(startDir, ".claude", "skills", "stale", "SKILL.md"),
		"---\nname: stale\ndescription: from the start directory\n---\n\nwrong project\n")

	workspace := t.TempDir()
	write(t, filepath.Join(workspace, ".claude", "skills", "ship", "SKILL.md"),
		"---\nname: ship\ndescription: from the workspace\n---\n\nthe workspace skill body\n")
	home := t.TempDir()

	registry := startupTestRegistry(t)
	section, _, _, skillList, _, _, err := buildSystemPrompt(startupTestConfig(), registry, workspace, home)
	if err != nil {
		t.Fatalf("buildSystemPrompt: %v", err)
	}

	names := map[string]bool{}
	for _, sk := range skillList {
		names[sk.Name] = true
		if sk.Name == "ship" && !strings.HasPrefix(sk.Path, workspace) {
			t.Errorf("ship loaded from %q, want it under the workspace %q", sk.Path, workspace)
		}
		if sk.Name == "ship" && !strings.Contains(sk.Body, "the workspace skill body") {
			t.Errorf("ship body = %q, want the workspace copy", sk.Body)
		}
	}
	if !names["ship"] {
		t.Error("the workspace skill did not load at startup")
	}
	if names["stale"] {
		t.Error("a skill from the start directory loaded; startup read the wrong project")
	}
	if !strings.Contains(section, "ship") || strings.Contains(section, "stale") {
		t.Errorf("prompt section = %q, want the workspace skill and not the start-directory one", section)
	}
	if !slicesContains(registry.Names(), "Skill") {
		t.Error("the Skill tool was not registered although a skill loaded")
	}
}

// The same Windows shape for custom commands: they share the root chain
// with skills, so they load from the workspace on the same terms, win on
// a name collision, and never see the start directory.
func TestStartupLoadsCommandsFromTheWorkspaceNotTheStartDirectory(t *testing.T) {
	startDir := t.TempDir()
	write(t, filepath.Join(startDir, ".claude", "commands", "deploy.md"),
		"---\ndescription: the wrong deploy\n---\n\nstart-dir body\n")

	workspace := t.TempDir()
	write(t, filepath.Join(workspace, ".claude", "commands", "deploy.md"),
		"---\ndescription: the workspace deploy\n---\n\nworkspace body\n")
	home := t.TempDir()
	write(t, filepath.Join(home, ".claude", "commands", "standup.md"),
		"---\ndescription: daily standup\n---\n\nWhat did I do yesterday?\n")

	registry := startupTestRegistry(t)
	_, _, _, _, cmdList, _, err := buildSystemPrompt(startupTestConfig(), registry, workspace, home)
	if err != nil {
		t.Fatalf("buildSystemPrompt: %v", err)
	}

	byName := map[string]string{}
	for _, cmd := range cmdList {
		byName[cmd.Name] = cmd.Body
	}
	if body := byName["deploy"]; !strings.Contains(body, "workspace body") {
		t.Errorf("deploy body = %q, want the workspace copy to win", body)
	}
	if body := byName["standup"]; !strings.Contains(body, "What did I do yesterday?") {
		t.Errorf("standup body = %q, want the global command beside the project ones", body)
	}
	for _, cmd := range cmdList {
		if cmd.Name == "deploy" && !strings.HasPrefix(cmd.Path, workspace) {
			t.Errorf("deploy loaded from %q, want it under the workspace %q", cmd.Path, workspace)
		}
	}
}

// The root chain still behaves at startup: .claude beats .opencode beats
// .localcode, an empty winner still wins, and the project and home roots
// are resolved independently.
func TestStartupKeepsTheRootChain(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".localcode", "skills", "home-skill", "SKILL.md"),
		"---\nname: home-skill\ndescription: the home skill\n---\n\nhome\n")

	workspace := t.TempDir()
	// .opencode holds a skill, but the empty .claude beside it wins, so
	// the skill must not load.
	write(t, filepath.Join(workspace, ".opencode", "skills", "shadowed", "SKILL.md"),
		"---\nname: shadowed\ndescription: never reached\n---\n\nno\n")
	if err := os.MkdirAll(filepath.Join(workspace, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	registry := startupTestRegistry(t)
	_, _, _, skillList, _, _, err := buildSystemPrompt(startupTestConfig(), registry, workspace, home)
	if err != nil {
		t.Fatalf("buildSystemPrompt: %v", err)
	}
	names := map[string]bool{}
	for _, sk := range skillList {
		names[sk.Name] = true
	}
	if names["shadowed"] {
		t.Error("a skill from a shadowed .opencode root loaded; the empty .claude winner must still win")
	}
	if !names["home-skill"] {
		t.Error("the home root stopped resolving independently of the project root")
	}

	// Without any .claude, .opencode answers: the chain beyond the first
	// root still works.
	workspace2 := t.TempDir()
	write(t, filepath.Join(workspace2, ".opencode", "skills", "op-skill", "SKILL.md"),
		"---\nname: op-skill\ndescription: opencode skill\n---\n\nop\n")
	write(t, filepath.Join(workspace2, ".localcode", "skills", "lc-shadowed", "SKILL.md"),
		"---\nname: lc-shadowed\ndescription: never reached\n---\n\nno\n")
	registry2 := startupTestRegistry(t)
	_, _, _, skillList2, _, _, err := buildSystemPrompt(startupTestConfig(), registry2, workspace2, home)
	if err != nil {
		t.Fatalf("buildSystemPrompt: %v", err)
	}
	names2 := map[string]bool{}
	for _, sk := range skillList2 {
		names2[sk.Name] = true
	}
	if !names2["op-skill"] {
		t.Error(".opencode did not answer when .claude is absent")
	}
	if names2["lc-shadowed"] {
		t.Error(".localcode leaked through a present .opencode root")
	}
}

// A project skill still overrides a same-named global one at startup: the
// body and description the model sees are the project's.
func TestStartupProjectSkillOverridesGlobal(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".claude", "skills", "lint", "SKILL.md"),
		"---\nname: lint\ndescription: the global linter\n---\n\nglobal body\n")
	workspace := t.TempDir()
	write(t, filepath.Join(workspace, ".claude", "skills", "lint-local", "SKILL.md"),
		"---\nname: lint\ndescription: the project linter\n---\n\nproject body\n")

	registry := startupTestRegistry(t)
	_, _, _, skillList, _, _, err := buildSystemPrompt(startupTestConfig(), registry, workspace, home)
	if err != nil {
		t.Fatalf("buildSystemPrompt: %v", err)
	}
	if len(skillList) != 1 {
		t.Fatalf("skills = %+v, want the single overridden lint", skillList)
	}
	if skillList[0].Description != "the project linter" || !strings.Contains(skillList[0].Body, "project body") {
		t.Errorf("skill = %+v, want the project's copy to win", skillList[0])
	}
}

// A malformed skill beside good ones must not stop startup: the good ones
// still load, the broken one is skipped, and buildSystemPrompt itself
// reports no error. The warning line it produces is pinned in
// internal/skills; here the requirement is that startup survives it.
func TestStartupSurvivesAMalformedSkill(t *testing.T) {
	workspace := t.TempDir()
	write(t, filepath.Join(workspace, ".claude", "skills", "broken", "SKILL.md"),
		"\n---\nname: broken\ndescription: blank first line\n---\n\nno\n")
	write(t, filepath.Join(workspace, ".claude", "skills", "fine", "SKILL.md"),
		"---\nname: fine\ndescription: loads anyway\n---\n\nfine body\n")
	home := t.TempDir()

	registry := startupTestRegistry(t)
	_, _, _, skillList, _, _, err := buildSystemPrompt(startupTestConfig(), registry, workspace, home)
	if err != nil {
		t.Fatalf("a malformed skill must not fail startup: %v", err)
	}
	if len(skillList) != 1 || skillList[0].Name != "fine" {
		t.Errorf("skills = %+v, want only the well-formed one beside the broken file", skillList)
	}
}

func slicesContains(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}
