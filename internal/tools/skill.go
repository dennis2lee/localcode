package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"localcode/internal/skills"
)

// SkillTool exposes progressive disclosure over a fixed set of skills
// loaded at startup: the model sees only name+description in the system
// prompt (see skills.SystemPromptSection) and calls this tool by name to
// fetch a skill's full SKILL.md body only when it's actually relevant.
type SkillTool struct {
	byName map[string]skills.Skill
}

func NewSkillTool(list []skills.Skill) SkillTool {
	byName := make(map[string]skills.Skill, len(list))
	for _, s := range list {
		byName[s.Name] = s
	}
	return SkillTool{byName: byName}
}

func (SkillTool) Name() string { return "Skill" }
func (SkillTool) Description() string {
	return "Load the full instructions for a named skill (see the skill index in the system prompt for available names)."
}
func (SkillTool) InputSchema() json.RawMessage {
	return schema(`{"name":{"type":"string"}}`, "name")
}
func (SkillTool) RequiresPermission(json.RawMessage) bool { return false }

func (t SkillTool) Execute(_ context.Context, input json.RawMessage) Result {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}
	}

	sk, ok := t.byName[args.Name]
	if !ok {
		return Result{Content: fmt.Sprintf("unknown skill %q", args.Name), IsError: true}
	}
	return Result{Content: sk.Body}
}
