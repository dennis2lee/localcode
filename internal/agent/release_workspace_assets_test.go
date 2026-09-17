package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localcode/internal/commands"
	"localcode/internal/skills"
	"localcode/internal/userdirs"
)

func writeAsset(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// wireWorkspaceReload is the composition cmd/localcode builds at startup:
// ReloadSkills re-reads the live default workspace through the same root
// chain, and a default-workspace move runs it. Replicated here rather
// than imported — package main is not importable — which is exactly the
// contract the wiring depends on: the hook reloads, the loop serves the
// new list.
func wireWorkspaceReload(t *testing.T, loop *Loop, home string) {
	t.Helper()
	loop.ReloadSkills = func() (string, error) {
		project := userdirs.At(loop.GetProjectDir())
		global := userdirs.At(home)
		list, err := skills.LoadAll(project.Skills, global.Skills)
		if err != nil {
			return "", err
		}
		section := ""
		if len(list) > 0 {
			section = skills.SystemPromptSection(list)
		}
		loop.SetSkills(list, section)
		cmdList, err := commands.LoadAll(project.Commands, global.Commands)
		if err != nil {
			return "", err
		}
		loop.SetCommands(cmdList)
		return "reloaded", nil
	}
	loop.OnProjectDirChanged = func() {
		if _, err := loop.ReloadSkills(); err != nil {
			t.Errorf("reload after workspace switch: %v", err)
		}
	}
}

// Moving the default workspace to a project loads that project's skills
// and custom commands with no manual step: the session the desktop opened
// in the install directory picks up the opened project's assets as soon
// as the client names it. The turn afterwards runs the new project's
// command, which is the consumer acting on the reloaded value rather
// than the load merely succeeding.
func TestSwitchingTheDefaultWorkspaceReloadsSkillsAndCommands(t *testing.T) {
	// The scripted provider, not a mock HTTP server: it observes what the
	// model was sent without binding a port.
	p := scriptedReply("done.")

	startDir := t.TempDir()
	writeAsset(t, filepath.Join(startDir, ".claude", "skills", "stale", "SKILL.md"),
		"---\nname: stale\ndescription: from the start directory\n---\n\nwrong project\n")
	writeAsset(t, filepath.Join(startDir, ".claude", "commands", "stale-cmd.md"),
		"---\ndescription: from the start directory\n---\n\nwrong project\n")

	workspace := t.TempDir()
	writeAsset(t, filepath.Join(workspace, ".claude", "skills", "ship", "SKILL.md"),
		"---\nname: ship\ndescription: from the workspace\n---\n\nthe workspace skill body\n")
	writeAsset(t, filepath.Join(workspace, ".claude", "commands", "deploy.md"),
		"---\ndescription: ship the workspace build\n---\n\nDeploy the workspace build: $ARGUMENTS\n")
	// Malformed, beside the good ones: the switch load must survive it.
	writeAsset(t, filepath.Join(workspace, ".claude", "skills", "broken", "SKILL.md"),
		"\n---\nname: broken\ndescription: blank first line\n---\n\nno\n")

	home := t.TempDir()
	writeAsset(t, filepath.Join(home, ".claude", "skills", "home-base", "SKILL.md"),
		"---\nname: home-base\ndescription: the global skill\n---\n\nglobal body\n")
	writeAsset(t, filepath.Join(home, ".claude", "commands", "standup.md"),
		"---\ndescription: daily standup\n---\n\nWhat did I do yesterday?\n")

	loop, store, _ := newSkillPathLoop(t, p, t.TempDir())
	wireWorkspaceReload(t, loop, home)

	// The session the client opened before naming a project: no recorded
	// workspace of its own, so it follows the default wherever it goes.
	const sid = "s1"
	if _, err := store.CreateSession(sid, "", "general-purpose", true); err != nil {
		t.Fatalf("create session: %v", err)
	}

	loop.SetProjectDir(startDir)
	if names := skillNames(loop.SkillList()); !names["stale"] {
		t.Fatalf("skills = %v, want the start directory loaded first", names)
	}
	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/deploy v1"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got := sentText(p); got != "" {
		t.Fatalf("the workspace command ran before its workspace was named: %s", got)
	}

	// The client names the project. No "/reset-skills" is sent.
	loop.SetProjectDir(workspace)

	names := skillNames(loop.SkillList())
	if !names["ship"] {
		t.Errorf("skills = %v, want the workspace skill after the switch", names)
	}
	if names["stale"] {
		t.Errorf("skills = %v, want the start directory's skill gone after the switch", names)
	}
	if !names["home-base"] {
		t.Errorf("skills = %v, want the global skill to survive the switch", names)
	}
	for _, sk := range loop.SkillList() {
		if sk.Name == "ship" && !strings.HasPrefix(sk.Path, workspace) {
			t.Errorf("ship loaded from %q, want it under the workspace %q", sk.Path, workspace)
		}
	}
	cmdNames := map[string]string{}
	for _, cmd := range loop.Commands {
		cmdNames[cmd.Name] = cmd.Body
	}
	if body := cmdNames["deploy"]; !strings.Contains(body, "Deploy the workspace build") {
		t.Errorf("commands = %v, want the workspace command after the switch", cmdNames)
	}
	if _, ok := cmdNames["stale-cmd"]; ok {
		t.Errorf("commands = %v, want the start directory's command gone after the switch", cmdNames)
	}
	if body := cmdNames["standup"]; !strings.Contains(body, "What did I do yesterday?") {
		t.Errorf("commands = %v, want the global command to survive the switch", cmdNames)
	}

	if err := loop.SendMessage(context.Background(), sid, "general-purpose", "/deploy v2"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if got := sentText(p); !strings.Contains(got, "Deploy the workspace build: v2") {
		t.Errorf("the reloaded command did not run; model was sent: %s", got)
	}

	if listing := replyTo(t, loop, sid, "/skill"); !strings.Contains(listing, "ship") {
		t.Errorf("/skill lists %q, want the workspace skill", listing)
	}
}

// Moving to the same directory runs no reload: the hook fires on a change
// of workspace, and re-reading the disk on every redundant call would
// thrash the list turns are reading for no new information.
func TestSettingTheSameWorkspaceRunsNoReload(t *testing.T) {
	loop, _, _ := newSkillPathLoop(t, scriptedReply("done."), t.TempDir())
	calls := 0
	loop.OnProjectDirChanged = func() { calls++ }

	dir := t.TempDir()
	loop.SetProjectDir(dir)
	loop.SetProjectDir(dir)
	if calls != 1 {
		t.Errorf("hook ran %d times for one real move plus a repeat, want 1", calls)
	}
}

func skillNames(list []skills.Skill) map[string]bool {
	names := map[string]bool{}
	for _, s := range list {
		names[s.Name] = true
	}
	return names
}
