package tool

import (
	"context"
	"fmt"
	"strings"

	"github.com/fpt/klein-cli/pkg/message"
)

// SpawnCallback is the function called when the spawn_agent tool is invoked.
// It runs a sub-agent with its own fresh conversation state and returns the result text.
type SpawnCallback func(ctx context.Context, task string, skillName string, allowedTools []string, maxIterations int) (string, error)

// SpawnAgentToolManager provides the spawn_agent tool.
type SpawnAgentToolManager struct {
	callback SpawnCallback
	tools    map[message.ToolName]message.Tool
}

// NewSpawnAgentToolManager creates a SpawnAgentToolManager with no callback set.
// Call SetCallback after constructing the parent agent.
func NewSpawnAgentToolManager() *SpawnAgentToolManager {
	m := &SpawnAgentToolManager{tools: make(map[message.ToolName]message.Tool)}
	m.registerTools()
	return m
}

// SetCallback wires the callback that executes sub-agents.
func (m *SpawnAgentToolManager) SetCallback(cb SpawnCallback) {
	m.callback = cb
}

func (m *SpawnAgentToolManager) GetTool(name message.ToolName) (message.Tool, bool) {
	t, ok := m.tools[name]
	return t, ok
}

func (m *SpawnAgentToolManager) GetTools() map[message.ToolName]message.Tool { return m.tools }

func (m *SpawnAgentToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	t, ok := m.tools[name]
	if !ok {
		return message.NewToolResultError(fmt.Sprintf("tool %q not found", name)), nil
	}
	return t.Handler()(ctx, args)
}

// RegisterTool satisfies domain.ToolManager.
func (m *SpawnAgentToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	m.tools[name] = &genericTool{
		name:        name,
		description: description,
		arguments:   arguments,
		handler:     handler,
	}
}

func (m *SpawnAgentToolManager) registerTools() {
	m.tools["spawn_agent"] = &spawnAgentTool{manager: m}
}

// spawnAgentTool implements message.Tool for spawn_agent.
type spawnAgentTool struct {
	manager *SpawnAgentToolManager
}

func (t *spawnAgentTool) RawName() message.ToolName { return "spawn_agent" }
func (t *spawnAgentTool) Name() message.ToolName    { return "spawn_agent" }

func (t *spawnAgentTool) Description() message.ToolDescription {
	return "Spawn a sub-agent with its own fresh conversation context to complete a focused task. " +
		"The sub-agent runs independently and returns its result as text. " +
		"Useful for parallelizable or isolated sub-tasks like codebase exploration, " +
		"file analysis, or targeted research. Sub-agents cannot spawn further agents."
}

func (t *spawnAgentTool) Arguments() []message.ToolArgument {
	return []message.ToolArgument{
		{
			Name:        "task",
			Description: "The task or question to give the sub-agent",
			Required:    true,
			Type:        "string",
		},
		{
			Name:        "skill",
			Description: "The skill name for the sub-agent (e.g. 'code'). Defaults to 'code'.",
			Required:    false,
			Type:        "string",
		},
		{
			Name:        "allowed_tools",
			Description: "Comma-separated list of tool names to make available to the sub-agent (e.g. 'Read,Glob,Grep,Bash'). Defaults to the skill's own allowed-tools.",
			Required:    false,
			Type:        "string",
		},
		{
			Name:        "max_iterations",
			Description: "Maximum number of reasoning iterations for the sub-agent (default: 10).",
			Required:    false,
			Type:        "number",
		},
	}
}

func (t *spawnAgentTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
		task, _ := args["task"].(string)
		if task == "" {
			return message.NewToolResultError("spawn_agent: 'task' argument is required"), nil
		}

		skillName, _ := args["skill"].(string)
		if skillName == "" {
			skillName = "code"
		}

		var allowedTools []string
		if raw, ok := args["allowed_tools"].(string); ok && raw != "" {
			for _, s := range strings.Split(raw, ",") {
				if trimmed := strings.TrimSpace(s); trimmed != "" {
					allowedTools = append(allowedTools, trimmed)
				}
			}
		}

		maxIterations := 0
		switch v := args["max_iterations"].(type) {
		case float64:
			maxIterations = int(v)
		case int:
			maxIterations = v
		}

		if t.manager.callback == nil {
			return message.NewToolResultError("spawn_agent: not available in this context"), nil
		}

		result, err := t.manager.callback(ctx, task, skillName, allowedTools, maxIterations)
		if err != nil {
			return message.NewToolResultError(fmt.Sprintf("spawn_agent: sub-agent failed: %v", err)), nil
		}
		return message.ToolResult{Text: result}, nil
	}
}
