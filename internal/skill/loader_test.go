package skill

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBuiltinSkills(t *testing.T) {
	skills, err := LoadBuiltinSkills()
	if err != nil {
		t.Fatalf("failed to load built-in skills: %v", err)
	}
	if len(skills) == 0 {
		t.Fatal("expected at least one built-in skill")
	}
}

func TestLoadBuiltinSkills_HasCodeSkill(t *testing.T) {
	skills, err := LoadBuiltinSkills()
	if err != nil {
		t.Fatalf("failed to load built-in skills: %v", err)
	}
	code, ok := skills["code"]
	if !ok {
		t.Fatal("expected 'code' skill in built-in skills")
	}
	if code.Name != "code" {
		t.Errorf("expected name 'code', got %q", code.Name)
	}
	if code.Description == "" {
		t.Error("expected non-empty description for code skill")
	}
	if len(code.AllowedTools) != 0 {
		t.Errorf("expected no allowed-tools for code skill (all tools), got %v", code.AllowedTools)
	}
	if code.Content == "" {
		t.Error("expected non-empty content for code skill")
	}
}


func TestLoadSkillsFromDir(t *testing.T) {
	// Create temp directory with a skill
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "test-skill")
	os.MkdirAll(skillDir, 0755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: test-skill
description: A test skill
allowed-tools: read_file
---

Test content.
`), 0644)

	skills, err := LoadSkillsFromDir(tmpDir, 1)
	if err != nil {
		t.Fatalf("failed to load skills from dir: %v", err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	s, ok := skills["test-skill"]
	if !ok {
		t.Fatal("expected 'test-skill' in loaded skills")
	}
	if s.Priority != 1 {
		t.Errorf("expected priority 1, got %d", s.Priority)
	}
	if len(s.AllowedTools) != 1 || s.AllowedTools[0] != "read_file" {
		t.Errorf("unexpected allowed tools: %v", s.AllowedTools)
	}
}

func TestLoadSkills_PriorityOverride(t *testing.T) {
	// Create project skills dir
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, ".claude", "skills", "my-skill")
	os.MkdirAll(projectDir, 0755)
	os.WriteFile(filepath.Join(projectDir, "SKILL.md"), []byte(`---
name: my-skill
description: Project version
---

Project content.
`), 0644)

	// LoadSkills uses workingDir to find .claude/skills/
	skills, err := LoadSkills(tmpDir)
	if err != nil {
		t.Fatalf("failed to load skills: %v", err)
	}

	// Should have built-in skills + project skill
	if _, ok := skills["code"]; !ok {
		t.Error("expected built-in 'code' skill")
	}
	if s, ok := skills["my-skill"]; !ok {
		t.Error("expected project 'my-skill' skill")
	} else if s.Priority != 3 {
		t.Errorf("expected project skill priority 3, got %d", s.Priority)
	}
}

func TestLoadSkills_EmptyDir(t *testing.T) {
	// LoadSkills with a dir that has no .claude/skills/
	tmpDir := t.TempDir()
	skills, err := LoadSkills(tmpDir)
	if err != nil {
		t.Fatalf("failed to load skills: %v", err)
	}
	// Should still have built-in skills
	if _, ok := skills["code"]; !ok {
		t.Error("expected built-in 'code' skill even with empty dir")
	}
}

func TestLoadSkillsFromDir_SkipNonSkillDirs(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a dir without SKILL.md
	os.MkdirAll(filepath.Join(tmpDir, "not-a-skill"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "not-a-skill", "README.md"), []byte("not a skill"), 0644)

	skills, err := LoadSkillsFromDir(tmpDir, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(skills))
	}
}
