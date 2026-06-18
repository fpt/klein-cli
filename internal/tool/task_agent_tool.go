package tool

import (
	"context"
	"fmt"

	"github.com/fpt/klein-cli/pkg/message"
)

// TaskCallback runs the agent identified by subagentType with the given prompt
// and returns the agent's final text. The implementation is provided by the
// owning *app.Agent via SetCallback.
type TaskCallback func(ctx context.Context, subagentType, prompt string) (string, error)

// TaskAgentToolManager exposes the `Task` tool, which mirrors Claude Code's
// built-in dispatcher for delegating work to a named subagent loaded from a
// plugin's agents/*.md or the project/user agents/ directory.
type TaskAgentToolManager struct {
	callback TaskCallback
	tools    map[message.ToolName]message.Tool
}

// NewTaskAgentToolManager constructs a manager with no callback wired. The
// callback is set after the parent *app.Agent exists because the callback
// closes over agent state (two-phase init).
func NewTaskAgentToolManager() *TaskAgentToolManager {
	m := &TaskAgentToolManager{tools: make(map[message.ToolName]message.Tool)}
	m.tools["Task"] = &taskAgentTool{manager: m}
	return m
}

// SetCallback wires the Task dispatcher.
func (m *TaskAgentToolManager) SetCallback(cb TaskCallback) { m.callback = cb }

func (m *TaskAgentToolManager) GetTool(name message.ToolName) (message.Tool, bool) {
	t, ok := m.tools[name]
	return t, ok
}

func (m *TaskAgentToolManager) GetTools() map[message.ToolName]message.Tool { return m.tools }

func (m *TaskAgentToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	t, ok := m.tools[name]
	if !ok {
		return message.NewToolResultError(fmt.Sprintf("tool %q not found", name)), nil
	}
	return t.Handler()(ctx, args)
}

func (m *TaskAgentToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	m.tools[name] = &genericTool{
		name:        name,
		description: description,
		arguments:   arguments,
		handler:     handler,
	}
}

// taskAgentTool implements message.Tool for Task.
type taskAgentTool struct {
	manager *TaskAgentToolManager
}

func (t *taskAgentTool) RawName() message.ToolName { return "Task" }
func (t *taskAgentTool) Name() message.ToolName    { return "Task" }

func (t *taskAgentTool) Description() message.ToolDescription {
	return "Delegate a task to a NAMED subagent loaded from a plugin's agents/*.md " +
		"or the project/user .claude/agents/*.md directory. The subagent name " +
		"(e.g. 'repo-searcher', 'pr-watcher', or scoped 'docs-for-ai:repo-searcher') " +
		"is passed as subagent_type. The subagent runs in its own context using the " +
		"system prompt and tool restrictions declared in its frontmatter, and returns " +
		"a final answer as text. Prefer this tool over spawn_agent whenever the workflow " +
		"references an agent by name (e.g. \"dispatch to repo-searcher\"). " +
		"Subagents cannot spawn further subagents."
}

func (t *taskAgentTool) Arguments() []message.ToolArgument {
	return []message.ToolArgument{
		{
			Name:        "subagent_type",
			Description: "Name of the subagent to invoke. Use the bare name when unambiguous, or the scoped form \"<plugin>:<agent>\" (e.g. \"github-watcher:pr-watcher\").",
			Required:    true,
			Type:        "string",
		},
		{
			Name:        "description",
			Description: "A short (3-5 word) description of the task for display.",
			Required:    false,
			Type:        "string",
		},
		{
			Name:        "prompt",
			Description: "The full task prompt for the subagent.",
			Required:    true,
			Type:        "string",
		},
	}
}

func (t *taskAgentTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
		agentName, _ := args["subagent_type"].(string)
		prompt, _ := args["prompt"].(string)
		if agentName == "" {
			return message.NewToolResultError("Task: 'subagent_type' is required"), nil
		}
		if prompt == "" {
			return message.NewToolResultError("Task: 'prompt' is required"), nil
		}
		if t.manager.callback == nil {
			return message.NewToolResultError("Task: not available in this context (no agents loaded)"), nil
		}

		result, err := t.manager.callback(ctx, agentName, prompt)
		if err != nil {
			return message.NewToolResultError(fmt.Sprintf("Task: subagent %q failed: %v", agentName, err)), nil
		}
		return message.ToolResult{Text: result}, nil
	}
}
