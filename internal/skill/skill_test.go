package skill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/pkg/message"
)

func TestParseSkillMD_WithFrontmatter(t *testing.T) {
	data := []byte(`---
name: test-skill
description: A test skill
allowed-tools: read_file, write_file, grep
argument-hint: "[filename]"
disable-model-invocation: true
---

This is the content.

Second paragraph.
`)
	s, err := ParseSkillMD(data, "/test/skills/test-skill/SKILL.md", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if s.Name != "test-skill" {
		t.Errorf("expected name 'test-skill', got %q", s.Name)
	}
	if s.Description != "A test skill" {
		t.Errorf("expected description 'A test skill', got %q", s.Description)
	}
	if len(s.AllowedTools) != 3 {
		t.Fatalf("expected 3 allowed tools, got %d", len(s.AllowedTools))
	}
	if s.AllowedTools[0] != "read_file" || s.AllowedTools[1] != "write_file" || s.AllowedTools[2] != "grep" {
		t.Errorf("unexpected allowed tools: %v", s.AllowedTools)
	}
	if s.ArgumentHint != "[filename]" {
		t.Errorf("expected argument hint '[filename]', got %q", s.ArgumentHint)
	}
	if !s.DisableModelInvocation {
		t.Error("expected disable-model-invocation to be true")
	}
	if !s.UserInvocable {
		t.Error("expected user-invocable to default to true")
	}
	if s.Priority != 1 {
		t.Errorf("expected priority 1, got %d", s.Priority)
	}
	if s.Content == "" {
		t.Error("expected non-empty content")
	}
}

func TestParseSkillMD_NoFrontmatter(t *testing.T) {
	data := []byte("Just plain markdown content.\n\nSecond paragraph.")
	s, err := ParseSkillMD(data, "/test/skills/plain/SKILL.md", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if s.Name != "plain" {
		t.Errorf("expected name 'plain' from directory, got %q", s.Name)
	}
	if s.Content != "Just plain markdown content.\n\nSecond paragraph." {
		t.Errorf("unexpected content: %q", s.Content)
	}
	if len(s.AllowedTools) != 0 {
		t.Errorf("expected no allowed tools, got %v", s.AllowedTools)
	}
}

func TestParseSkillMD_EmptyAllowedTools(t *testing.T) {
	data := []byte(`---
name: no-tools
description: No tools specified
---

Content here.
`)
	s, err := ParseSkillMD(data, "/test/no-tools/SKILL.md", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s.AllowedTools) != 0 {
		t.Errorf("expected no allowed tools, got %v", s.AllowedTools)
	}
}

func TestParseSkillMD_UserInvocableFalse(t *testing.T) {
	data := []byte(`---
name: background
user-invocable: false
---

Background knowledge.
`)
	s, err := ParseSkillMD(data, "/test/background/SKILL.md", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.UserInvocable {
		t.Error("expected user-invocable to be false")
	}
}

func TestParseSkillMD_DescriptionFromContent(t *testing.T) {
	data := []byte(`---
name: auto-desc
---

This first paragraph becomes the description.

This does not.
`)
	s, err := ParseSkillMD(data, "/test/auto-desc/SKILL.md", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Description != "This first paragraph becomes the description." {
		t.Errorf("expected description from first paragraph, got %q", s.Description)
	}
}

func TestParseSkillMD_NameFromDirectory(t *testing.T) {
	data := []byte(`---
description: Test
---

Content.
`)
	s, err := ParseSkillMD(data, "/path/to/my-skill/SKILL.md", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Name != "my-skill" {
		t.Errorf("expected name 'my-skill' from directory, got %q", s.Name)
	}
}

func TestSkill_RenderContent_Arguments(t *testing.T) {
	s := &Skill{Content: "Process $ARGUMENTS now."}
	result := s.RenderContent("file.go", "/work")
	if result != "Process file.go now." {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestSkill_RenderContent_PositionalArgs(t *testing.T) {
	s := &Skill{Content: "Migrate $0 from $1 to $2."}
	result := s.RenderContent("SearchBar React Vue", "/work")
	if result != "Migrate SearchBar from React to Vue." {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestSkill_RenderContent_ArgumentsBracket(t *testing.T) {
	s := &Skill{Content: "First: $ARGUMENTS[0], second: $ARGUMENTS[1]."}
	result := s.RenderContent("hello world", "/work")
	if result != "First: hello, second: world." {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestSkill_RenderContent_WorkingDir(t *testing.T) {
	s := &Skill{Content: "Dir: {{workingDir}}"}
	result := s.RenderContent("", "/my/project")
	if result != "Dir: /my/project" {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestSkill_RenderContent_AppendArguments(t *testing.T) {
	s := &Skill{Content: "No placeholder here."}
	result := s.RenderContent("extra args", "/work")
	expected := "No placeholder here.\nARGUMENTS: extra args"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestSkill_RenderContent_NoAppendWhenEmpty(t *testing.T) {
	s := &Skill{Content: "No placeholder here."}
	result := s.RenderContent("", "/work")
	if result != "No placeholder here." {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestSkill_RenderContent_AtFileExpansion(t *testing.T) {
	// Create a temp file
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.md")
	os.WriteFile(testFile, []byte("file content here"), 0644)

	s := &Skill{Content: "Before\n@test.md\nAfter"}
	result := s.RenderContent("", tmpDir)
	if result != "Before\n----- BEGIN test.md -----\nfile content here\n----- END test.md -----\nAfter" {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestSkill_RenderContent_AtFileMissing(t *testing.T) {
	s := &Skill{Content: "Before\n@nonexistent.md\nAfter"}
	result := s.RenderContent("", "/tmp")
	if result != "Before\nAfter" {
		t.Errorf("expected missing file to be dropped, got: %q", result)
	}
}

func TestBuildSkillCatalog_Empty(t *testing.T) {
	result := BuildSkillCatalog(SkillMap{})
	if result != "" {
		t.Errorf("expected empty string for empty skills, got %q", result)
	}
}

func TestBuildSkillCatalog_Multiple(t *testing.T) {
	skills := SkillMap{
		"code": {Name: "code", Description: "Coding assistant"},
		"claw": {Name: "claw", Description: "Messaging assistant"},
	}
	result := BuildSkillCatalog(skills)
	if !strings.Contains(result, "- **code**: Coding assistant") {
		t.Errorf("expected code skill in catalog, got:\n%s", result)
	}
	if !strings.Contains(result, "- **claw**: Messaging assistant") {
		t.Errorf("expected claw skill in catalog, got:\n%s", result)
	}
	// Verify alphabetical order: claw before code
	clawIdx := strings.Index(result, "**claw**")
	codeIdx := strings.Index(result, "**code**")
	if clawIdx >= codeIdx {
		t.Error("expected skills to be sorted alphabetically")
	}
	if !strings.Contains(result, "read_skill") {
		t.Error("expected catalog to mention read_skill tool")
	}
}

func TestBuildSkillCatalog_NoDescription(t *testing.T) {
	skills := SkillMap{
		"empty": {Name: "empty", Description: ""},
	}
	result := BuildSkillCatalog(skills)
	if !strings.Contains(result, "(no description)") {
		t.Errorf("expected '(no description)' for empty description, got:\n%s", result)
	}
}

// Mock tool manager for FilteredToolManager tests
type mockToolManager struct {
	tools map[message.ToolName]message.Tool
}

func (m *mockToolManager) GetTools() map[message.ToolName]message.Tool { return m.tools }
func (m *mockToolManager) CallTool(_ context.Context, name message.ToolName, _ message.ToolArgumentValues) (message.ToolResult, error) {
	return message.ToolResult{Text: "called:" + string(name)}, nil
}
func (m *mockToolManager) RegisterTool(message.ToolName, message.ToolDescription, []message.ToolArgument, func(context.Context, message.ToolArgumentValues) (message.ToolResult, error)) {
}

type mockTool struct {
	name message.ToolName
}

func (t *mockTool) RawName() message.ToolName                    { return t.name }
func (t *mockTool) Name() message.ToolName                       { return t.name }
func (t *mockTool) Description() message.ToolDescription         { return "mock" }
func (t *mockTool) Arguments() []message.ToolArgument            { return nil }
func (t *mockTool) Handler() func(context.Context, message.ToolArgumentValues) (message.ToolResult, error) {
	return nil
}

func newMockToolManager(names ...string) *mockToolManager {
	tools := make(map[message.ToolName]message.Tool)
	for _, n := range names {
		tools[message.ToolName(n)] = &mockTool{name: message.ToolName(n)}
	}
	return &mockToolManager{tools: tools}
}

func TestFilteredToolManager_AllTools(t *testing.T) {
	s := &Skill{AllowedTools: nil} // empty = all tools
	source := newMockToolManager("read_file", "write_file", "grep")
	result := s.FilterTools(source)
	if result != source {
		t.Error("expected source manager returned directly when no allowed-tools")
	}
}

func TestFilteredToolManager_SubsetTools(t *testing.T) {
	source := newMockToolManager("read_file", "write_file", "grep", "bash")
	fm := NewFilteredToolManager(source, []string{"read_file", "grep"})
	tools := fm.GetTools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if _, ok := tools["read_file"]; !ok {
		t.Error("expected read_file in filtered tools")
	}
	if _, ok := tools["grep"]; !ok {
		t.Error("expected grep in filtered tools")
	}
	if _, ok := tools["write_file"]; ok {
		t.Error("write_file should not be in filtered tools")
	}
}

func TestFilteredToolManager_UnknownToolIgnored(t *testing.T) {
	source := newMockToolManager("read_file", "write_file")
	fm := NewFilteredToolManager(source, []string{"read_file", "NonExistent"})
	tools := fm.GetTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
}

func TestFilteredToolManager_CallTool(t *testing.T) {
	source := newMockToolManager("read_file", "write_file")
	fm := NewFilteredToolManager(source, []string{"read_file"})

	result, err := fm.CallTool(context.Background(), "read_file", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Text != "called:read_file" {
		t.Errorf("unexpected result: %q", result.Text)
	}
}

func TestFilteredToolManager_CallToolDenied(t *testing.T) {
	source := newMockToolManager("read_file", "write_file")
	fm := NewFilteredToolManager(source, []string{"read_file"})

	result, err := fm.CallTool(context.Background(), "write_file", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected error in result for denied tool")
	}
}
