package tool

import (
	"context"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/internal/skill"
	"github.com/fpt/klein-cli/pkg/message"
)

func newTestSkillMap() skill.SkillMap {
	return skill.SkillMap{
		"code": {
			Name:        "code",
			Description: "Comprehensive coding assistant",
			Content:     "You are a coding assistant.\n\nWorking Directory: {{workingDir}}",
		},
		"respond": {
			Name:         "respond",
			Description:  "Direct knowledge-based responses",
			AllowedTools: []string{"Read", "Write", "read_skill"},
			Content:      "Provide a direct response.",
		},
	}
}

func TestSkillToolManager_GetTools(t *testing.T) {
	m := NewSkillToolManager(newTestSkillMap(), "/work")
	tools := m.GetTools()
	if _, ok := tools["read_skill"]; !ok {
		t.Error("expected read_skill tool to be registered")
	}
	if len(tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(tools))
	}
}

func TestSkillToolManager_ReadSkill_Existing(t *testing.T) {
	m := NewSkillToolManager(newTestSkillMap(), "/myproject")
	result, err := m.CallTool(context.Background(), "read_skill", message.ToolArgumentValues{"name": "code"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected tool error: %s", result.Error)
	}
	if !strings.Contains(result.Text, "# Skill: code") {
		t.Error("expected skill header in result")
	}
	if !strings.Contains(result.Text, "Comprehensive coding assistant") {
		t.Error("expected description in result")
	}
	if !strings.Contains(result.Text, "/myproject") {
		t.Error("expected workingDir to be rendered in content")
	}
}

func TestSkillToolManager_ReadSkill_NotFound(t *testing.T) {
	m := NewSkillToolManager(newTestSkillMap(), "/work")
	result, err := m.CallTool(context.Background(), "read_skill", message.ToolArgumentValues{"name": "nonexistent"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Fatal("expected error for nonexistent skill")
	}
	if !strings.Contains(result.Error, "not found") {
		t.Errorf("expected 'not found' in error, got: %s", result.Error)
	}
	if !strings.Contains(result.Error, "code") || !strings.Contains(result.Error, "respond") {
		t.Errorf("expected available skills listed in error, got: %s", result.Error)
	}
}

func TestSkillToolManager_ReadSkill_CaseInsensitive(t *testing.T) {
	m := NewSkillToolManager(newTestSkillMap(), "/work")
	result, err := m.CallTool(context.Background(), "read_skill", message.ToolArgumentValues{"name": "CODE"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected tool error: %s", result.Error)
	}
	if !strings.Contains(result.Text, "# Skill: code") {
		t.Error("expected skill content for case-insensitive lookup")
	}
}

func TestSkillToolManager_ReadSkill_EmptyName(t *testing.T) {
	m := NewSkillToolManager(newTestSkillMap(), "/work")
	result, err := m.CallTool(context.Background(), "read_skill", message.ToolArgumentValues{"name": ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Fatal("expected error for empty name")
	}
}

func TestSkillToolManager_ReadSkill_MissingParam(t *testing.T) {
	m := NewSkillToolManager(newTestSkillMap(), "/work")
	result, err := m.CallTool(context.Background(), "read_skill", message.ToolArgumentValues{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Fatal("expected error for missing name param")
	}
}

func TestSkillToolManager_ReadSkill_AllowedToolsShown(t *testing.T) {
	m := NewSkillToolManager(newTestSkillMap(), "/work")
	result, err := m.CallTool(context.Background(), "read_skill", message.ToolArgumentValues{"name": "respond"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Text, "Allowed Tools:") {
		t.Error("expected allowed tools listed for respond skill")
	}
}
