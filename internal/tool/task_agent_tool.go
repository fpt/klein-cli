package tool

import (
	"context"
	"fmt"
	"strings"

	"github.com/fpt/klein-cli/pkg/message"
)

// TaskCallback runs the agent identified by subagentType with the given prompt
// and returns the agent's final text. The implementation is provided by the
// owning *app.Agent via SetCallback.
type TaskCallback func(ctx context.Context, subagentType, prompt string) (string, error)

// AgentCatalogEntry describes one dispatchable subagent for the Task tool's
// description. Name is the identifier the model must pass as subagent_type —
// the bare name when unambiguous, otherwise the scoped "<plugin>:<agent>" form.
type AgentCatalogEntry struct {
	Name        string
	Description string
	Tools       []string // empty = inherits all tools
}

// AgentCatalogProvider returns the currently loaded subagents. It is called
// lazily on every Description() so the listing reflects agents registered
// after the tool manager was constructed (plugins load after the Agent exists).
type AgentCatalogProvider func() []AgentCatalogEntry

// TaskAgentToolManager exposes the `Task` tool, which mirrors Claude Code's
// built-in dispatcher for delegating work to a named subagent loaded from a
// plugin's agents/*.md or the project/user agents/ directory.
type TaskAgentToolManager struct {
	callback TaskCallback
	catalog  AgentCatalogProvider
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

// SetCatalogProvider wires the source of the available-agents listing embedded
// in the tool description. Without it the tool advertises that no subagents
// are loaded.
func (m *TaskAgentToolManager) SetCatalogProvider(p AgentCatalogProvider) { m.catalog = p }

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
	var b strings.Builder
	b.WriteString("Delegate a task to a NAMED subagent. The subagent runs in its own " +
		"context using the system prompt and tool restrictions declared in its " +
		"definition, and returns a final answer as text. Prefer this tool over " +
		"spawn_agent whenever a named agent fits the task. " +
		"Subagents cannot spawn further subagents.\n\n")

	entries := t.manager.agentCatalog()
	if len(entries) == 0 {
		b.WriteString("No subagents are currently loaded, so this tool cannot be used. " +
			"Use spawn_agent instead, which dispatches to a built-in skill.")
		return message.ToolDescription(b.String())
	}

	b.WriteString("Available agents (pass the name exactly as listed as subagent_type):\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "- %s: %s (Tools: %s)\n", e.Name, e.Description, formatAgentTools(e.Tools))
	}

	b.WriteString("\nBrief the subagent as you would a colleague who has not seen this " +
		"conversation: state the goal, what you already ruled out, and how long an " +
		"answer you want. It shares no context with you. To fan out over independent " +
		"questions, issue several Task calls in a single message — they run in parallel.")
	return message.ToolDescription(b.String())
}

// agentCatalog returns the loaded subagents, or nil when no provider is wired.
func (m *TaskAgentToolManager) agentCatalog() []AgentCatalogEntry {
	if m.catalog == nil {
		return nil
	}
	return m.catalog()
}

// formatAgentTools renders an entry's tool access for the listing. An empty
// allowlist means the agent inherits every tool the parent has.
func formatAgentTools(tools []string) string {
	if len(tools) == 0 {
		return "All tools"
	}
	return strings.Join(tools, ", ")
}

func (t *taskAgentTool) Arguments() []message.ToolArgument {
	return []message.ToolArgument{
		{
			Name: "subagent_type",
			Description: "Name of the subagent to invoke, exactly as listed under " +
				"\"Available agents\" in this tool's description. Names containing a " +
				"colon are the scoped \"<plugin>:<agent>\" form and must be passed in full.",
			Required: true,
			Type:     "string",
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
