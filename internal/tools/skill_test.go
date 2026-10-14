package tools

import (
	"context"
	"encoding/json"
	"testing"

	"localcode/internal/skills"
)

func TestSkillToolExecute(t *testing.T) {
	tool := NewSkillTool([]skills.Skill{
		{Name: "pdf", Description: "PDF stuff", Body: "full pdf instructions"},
	})

	input, _ := json.Marshal(map[string]string{"name": "pdf"})
	result := tool.Execute(context.Background(), input)

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if result.Content != "full pdf instructions" {
		t.Errorf("content = %q, want %q", result.Content, "full pdf instructions")
	}
}

func TestSkillToolUnknownName(t *testing.T) {
	tool := NewSkillTool([]skills.Skill{{Name: "pdf", Body: "..."}})

	input, _ := json.Marshal(map[string]string{"name": "does-not-exist"})
	result := tool.Execute(context.Background(), input)

	if !result.IsError {
		t.Error("expected an error for an unknown skill name")
	}
}

func TestSkillToolRequiresNoPermission(t *testing.T) {
	tool := NewSkillTool(nil)
	if tool.RequiresPermission(nil) {
		t.Error("Skill tool should never require permission")
	}
}

// The tool finds a skill the way `/name` does: an exact name first, then
// one that differs only in case. The loader keeps one skill per name
// compared that way, so the model asking for "PDF" reaches the skill a
// person typing /PDF runs, rather than "unknown skill".
func TestSkillToolFindsANameWithoutRegardToCase(t *testing.T) {
	tool := NewSkillTool([]skills.Skill{
		{Name: "pdf", Body: "lower"},
		{Name: "Report", Body: "report body"},
	})
	for name, want := range map[string]string{"pdf": "lower", "PDF": "lower", "report": "report body"} {
		input, _ := json.Marshal(map[string]string{"name": name})
		if result := tool.Execute(context.Background(), input); result.IsError || result.Content != want {
			t.Errorf("Skill(%q) = %q (error %v), want %q", name, result.Content, result.IsError, want)
		}
	}
}
