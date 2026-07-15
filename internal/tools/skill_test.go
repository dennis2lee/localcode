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
