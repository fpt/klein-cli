package tool

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/fpt/klein-cli/internal/skill"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

// SkillToolManager provides the read_skill tool for proactive skill discovery.
type SkillToolManager struct {
	tools      map[message.ToolName]message.Tool
	skills     skill.SkillMap
	workingDir string
}

// NewSkillToolManager creates a new skill tool manager.
func NewSkillToolManager(skills skill.SkillMap, workingDir string) *SkillToolManager {
	m := &SkillToolManager{
		tools:      make(map[message.ToolName]message.Tool),
		skills:     skills,
		workingDir: workingDir,
	}
	m.registerTools()
	return m
}

func (m *SkillToolManager) registerTools() {
	m.RegisterTool("read_skill",
		"Read the full content of a skill by name. Returns the skill's instructions and guidelines. Use this to understand what a skill does before following its instructions.",
		[]message.ToolArgument{
			{Name: "name", Description: "Name of the skill to read (case-insensitive)", Required: true, Type: "string"},
		},
		m.handleReadSkill,
	)
}

func (m *SkillToolManager) handleReadSkill(_ context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	nameArg, ok := args["name"].(string)
	if !ok || nameArg == "" {
		return message.NewToolResultError("name parameter is required"), nil
	}

	key := strings.ToLower(nameArg)
	s, exists := m.skills[key]
	if !exists {
		available := m.availableSkillNames()
		return message.NewToolResultError(
			fmt.Sprintf("skill '%s' not found. Available skills: %s", nameArg, strings.Join(available, ", ")),
		), nil
	}

	rendered := s.RenderContent("", m.workingDir)
	if rendered == "" {
		return message.NewToolResultText(fmt.Sprintf("Skill '%s' has no content.", s.Name)), nil
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("# Skill: %s\n", s.Name))
	if s.Description != "" {
		result.WriteString(fmt.Sprintf("Description: %s\n", s.Description))
	}
	if len(s.AllowedTools) > 0 {
		result.WriteString(fmt.Sprintf("Allowed Tools: %s\n", strings.Join(s.AllowedTools, ", ")))
	}
	result.WriteString("\n---\n\n")
	result.WriteString(rendered)

	return message.NewToolResultText(result.String()), nil
}

func (m *SkillToolManager) availableSkillNames() []string {
	names := make([]string, 0, len(m.skills))
	for name := range m.skills {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Implement domain.ToolManager interface

func (m *SkillToolManager) GetTools() map[message.ToolName]message.Tool {
	return m.tools
}

func (m *SkillToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	tool, exists := m.tools[name]
	if !exists {
		return message.NewToolResultError(fmt.Sprintf("tool '%s' not found", name)), nil
	}
	return tool.Handler()(ctx, args)
}

func (m *SkillToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	m.tools[name] = &skillTool{
		name:        name,
		description: description,
		arguments:   arguments,
		handler:     handler,
	}
}

// Verify interface compliance
var _ domain.ToolManager = (*SkillToolManager)(nil)

// skillTool implements the message.Tool interface.
type skillTool struct {
	name        message.ToolName
	description message.ToolDescription
	arguments   []message.ToolArgument
	handler     func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)
}

func (t *skillTool) RawName() message.ToolName        { return t.name }
func (t *skillTool) Name() message.ToolName            { return t.name }
func (t *skillTool) Description() message.ToolDescription { return t.description }
func (t *skillTool) Arguments() []message.ToolArgument { return t.arguments }
func (t *skillTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return t.handler
}
