package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/repository"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

// Type aliases for convenience
type TodoItem = repository.TodoItem

// TodoToolManager provides task management capabilities
type TodoToolManager struct {
	tools          map[message.ToolName]message.Tool
	todoRepository repository.TodoRepository
}

// NewTodoToolManagerWithRepository creates a new todo tool manager with injected repository
func NewTodoToolManagerWithRepository(repo repository.TodoRepository) *TodoToolManager {
	manager := &TodoToolManager{
		tools:          make(map[message.ToolName]message.Tool),
		todoRepository: repo,
	}

	// Load existing todos
	_ = repo.Load()

	// Register todo tools
	manager.registerTodoTools()

	// Register task tools
	manager.registerTaskTools()

	return manager
}

// NewTodoToolManager creates a new todo tool manager using user config
func NewTodoToolManager(projectPath string) *TodoToolManager {
	// Get user configuration - this must succeed
	userConfig, err := config.DefaultUserConfig()
	if err != nil {
		logger.Error("Failed to create user config directory", "error", err)
		logger.Error("klein requires $HOME/.klein/ directory access")
		os.Exit(1)
	}

	// Get project-specific todo file - this must also succeed
	todoFilePath, err := userConfig.GetProjectTodoFile(projectPath)
	if err != nil {
		logger.Error("Failed to get project todo file", "error", err)
		logger.Error("Cannot create project directory in $HOME/.klein/projects/")
		os.Exit(1)
	}

	// Create file-based repository and inject it
	repo := repository.NewFileTodoRepository(todoFilePath)

	return NewTodoToolManagerWithRepository(repo)
}

// NewTodoToolManagerWithPath creates a new todo tool manager with a specific file path
func NewTodoToolManagerWithPath(todoFilePath string) *TodoToolManager {
	repo := repository.NewFileTodoRepository(todoFilePath)

	return NewTodoToolManagerWithRepository(repo)
}

// NewInMemoryTodoToolManager creates a new todo tool manager that only stores data in memory (no persistence)
func NewInMemoryTodoToolManager() *TodoToolManager {
	repo := repository.NewInMemoryTodoRepository()

	return NewTodoToolManagerWithRepository(repo)
}

// Implement domain.ToolManager interface
func (m *TodoToolManager) GetTool(name message.ToolName) (message.Tool, bool) {
	tool, exists := m.tools[name]
	return tool, exists
}

func (m *TodoToolManager) GetTools() map[message.ToolName]message.Tool {
	return m.tools
}

func (m *TodoToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	tool, exists := m.tools[name]
	if !exists {
		return message.NewToolResultError(fmt.Sprintf("tool %s not found", name)), nil
	}

	handler := tool.Handler()
	return handler(ctx, args)
}

func (m *TodoToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, args []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	tool := &todoTool{
		name:        name,
		description: description,
		arguments:   args,
		handler:     handler,
	}
	m.tools[name] = tool
}

// IsAllCompleted reports whether there are todos and all of them are completed
func (m *TodoToolManager) IsAllCompleted() bool {
	items := m.todoRepository.GetItems()
	if len(items) == 0 {
		return false
	}
	for _, it := range items {
		st := it.Status
		if st == "done" {
			st = "completed"
		}
		if st != "completed" {
			return false
		}
	}
	return true
}

// GetToolState implements domain.ToolStateProvider.
// Returns a compact todo status summary so the model knows current task progress.
func (m *TodoToolManager) GetToolState() string {
	items := m.todoRepository.GetItems()
	if len(items) == 0 {
		return ""
	}

	counts := map[string]int{}
	for _, item := range items {
		st := item.Status
		if st == "done" {
			st = "completed"
		}
		counts[st]++
	}

	total := len(items)
	ann := fmt.Sprintf("Todo list: %d items", total)

	var parts []string
	for _, st := range []string{"in_progress", "pending", "completed"} {
		if n := counts[st]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, st))
		}
	}
	if len(parts) > 0 {
		ann += " (" + joinParts(parts) + ")"
	}

	if m.IsAllCompleted() {
		ann += " -- all completed"
	}

	return ann
}

func joinParts(parts []string) string {
	result := parts[0]
	for i := 1; i < len(parts); i++ {
		result += ", " + parts[i]
	}
	return result
}

// Compile-time check that TodoToolManager implements ToolStateProvider.
var _ domain.ToolStateProvider = (*TodoToolManager)(nil)

// registerTodoTools registers all todo management tools
func (m *TodoToolManager) registerTodoTools() {
	// TodoWrite - Write/update todo list
	// Note: TodoRead removed - todos are injected into prompt context instead
	m.RegisterTool("todo_write", "Write or update the todo list with tasks and their status. Use statuses: pending, in_progress, completed (accepts 'done' as completed). Keep ≤5 items.",
		[]message.ToolArgument{
			{
				Name:        "todos",
				Description: "Array of todo items with content, status, priority, and id",
				Required:    true,
				Type:        "array",
			},
		},
		m.handleTodoWrite)
}

// registerTaskTools registers small compatibility stubs for task tools
func (m *TodoToolManager) registerTaskTools() {
	// exit_plan_mode: acknowledge plan and signal ready
	m.RegisterTool("exit_plan_mode", "Acknowledge plan and exit planning mode (stub).",
		[]message.ToolArgument{
			{Name: "plan", Description: "Concise implementation plan", Required: true, Type: "string"},
		}, m.handleExitPlanMode)

	// Task: sub-agent launcher (stub)
	m.RegisterTool("Task", "Launch a sub-agent (stub). Not supported; use Glob/Grep/Read/WebFetch directly.",
		[]message.ToolArgument{
			{Name: "description", Description: "Short task description", Required: true, Type: "string"},
			{Name: "prompt", Description: "Detailed task for the agent", Required: true, Type: "string"},
		}, m.handleTaskStub)
}

// handleExitPlanMode acknowledges a plan and indicates readiness to implement
func (m *TodoToolManager) handleExitPlanMode(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	plan, _ := args["plan"].(string)
	msg := "Plan acknowledged. Exiting plan mode; ready to implement."
	if plan != "" {
		msg = fmt.Sprintf("Plan acknowledged. Ready to implement.\n\nPlan:\n%s", plan)
	}
	return message.NewToolResultText(msg), nil
}

// handleTaskStub informs that the Task sub-agent is not supported
func (m *TodoToolManager) handleTaskStub(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	desc, _ := args["description"].(string)
	msg := "Task sub-agent is not supported in this build. Use Glob/Grep to search, Read/LS/Edit/Write for files, and WebFetch for URLs."
	if desc != "" {
		msg = fmt.Sprintf("Task not supported. Description: %q. Use Glob/Grep/Read/WebFetch directly.", desc)
	}
	return message.NewToolResultText(msg), nil
}

// TodoWrite handler
func (m *TodoToolManager) handleTodoWrite(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	todosArg, ok := args["todos"]
	if !ok {
		return message.NewToolResultError("todos parameter is required"), nil
	}

	// Handle different input formats
	var todoItems []TodoItem

	// Try to parse as JSON array first
	if todosJSON, ok := todosArg.(string); ok {
		if err := json.Unmarshal([]byte(todosJSON), &todoItems); err != nil {
			return message.NewToolResultError(fmt.Sprintf("failed to parse todos JSON: %v", err)), nil
		}
	} else if todosSlice, ok := todosArg.([]interface{}); ok {
		// Handle slice of interfaces (from structured LLM output)
		for _, item := range todosSlice {
			if todoMap, ok := item.(map[string]interface{}); ok {
				todoItem := TodoItem{
					Created: time.Now().Format(time.RFC3339),
					Updated: time.Now().Format(time.RFC3339),
				}

				if id, ok := todoMap["id"].(string); ok {
					todoItem.ID = id
				}
				if content, ok := todoMap["content"].(string); ok {
					todoItem.Content = content
				}
				if status, ok := todoMap["status"].(string); ok {
					todoItem.Status = status
				}
				if priority, ok := todoMap["priority"].(string); ok {
					todoItem.Priority = priority
				}

				// Validate required fields
				if todoItem.ID == "" || todoItem.Content == "" || todoItem.Status == "" || todoItem.Priority == "" {
					return message.NewToolResultError("all todo items must have id, content, status, and priority"), nil
				}

				todoItems = append(todoItems, todoItem)
			}
		}
	} else {
		return message.NewToolResultError("todos parameter must be a JSON array or array of objects"), nil
	}

	// Enforce maximum of 5 todos for focus and clarity
	if len(todoItems) > 5 {
		return message.NewToolResultError("Too many todo items. Please limit to 5 items or fewer for better focus and management."), nil
	}

	// Normalize and validate todo items
	inProgressCount := 0
	for i, item := range todoItems {
		if item.ID == "" || item.Content == "" {
			return message.NewToolResultError("all todo items must have id and content"), nil
		}
		// Normalize status: map 'done' -> 'completed'
		if item.Status == "done" {
			item.Status = "completed"
		}
		if item.Status != "pending" && item.Status != "in_progress" && item.Status != "completed" {
			return message.NewToolResultError(fmt.Sprintf("invalid status '%s', must be pending, in_progress, or completed", item.Status)), nil
		}
		if item.Status == "in_progress" {
			inProgressCount++
		}
		if item.Priority != "high" && item.Priority != "medium" && item.Priority != "low" {
			return message.NewToolResultError(fmt.Sprintf("invalid priority '%s', must be high, medium, or low", item.Priority)), nil
		}
		// write-back normalization
		todoItems[i] = item
	}

	// Enforce at most one in_progress
	if inProgressCount > 1 {
		return message.NewToolResultError("Only one todo may be 'in_progress' at a time. Please adjust statuses and try again."), nil
	}

	// Update todo repository
	m.todoRepository.SetItems(todoItems)
	m.todoRepository.SetUpdated(time.Now().Format(time.RFC3339))

	// Save to repository
	if err := m.todoRepository.Save(); err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to save todos: %v", err)), nil
	}

	// Generate summary response
	statusCounts := make(map[string]int)
	priorityCounts := make(map[string]int)

	for _, item := range todoItems {
		statusCounts[item.Status]++
		priorityCounts[item.Priority]++
	}

	summary := fmt.Sprintf("Successfully updated todo list with %d items:\n", len(todoItems))
	summary += fmt.Sprintf("- Status: %d pending, %d in_progress, %d done\n",
		statusCounts["pending"], statusCounts["in_progress"], statusCounts["done"])
	summary += fmt.Sprintf("- Priority: %d high, %d medium, %d low",
		priorityCounts["high"], priorityCounts["medium"], priorityCounts["low"])

	return message.NewToolResultText(summary), nil
}

// GetTodosForPrompt returns formatted todos for injection into prompt context
func (m *TodoToolManager) GetTodosForPrompt() string {
	items := m.todoRepository.GetItems()
	if len(items) == 0 {
		return ""
	}

	// Format todos for prompt injection
	var result string
	result += fmt.Sprintf("Current Todo List (%d items):\n\n", len(items))

	// Group by status for better readability (normalize legacy 'done' → 'completed')
	statusGroups := map[string][]TodoItem{
		"in_progress": {},
		"pending":     {},
		"completed":   {},
	}

	for _, item := range items {
		status := item.Status
		if status == "done" {
			status = "completed"
		}
		statusGroups[status] = append(statusGroups[status], item)
	}

	// Display in_progress first, then pending, then completed
	for _, status := range []string{"in_progress", "pending", "completed"} {
		items := statusGroups[status]
		if len(items) == 0 {
			continue
		}

		result += fmt.Sprintf("## %s (%d items):\n", status, len(items))
		for _, item := range items {
			// Use raw API format for LLM consumption (not display format with emojis)
			// This ensures LLM uses correct format when calling todo_write tool
			normalized := item.Status
			if normalized == "done" {
				normalized = "completed"
			}
			result += fmt.Sprintf("- [%s] %s - %s (ID: %s)\n", item.Priority, item.Content, normalized, item.ID)
		}
		result += "\n"
	}

	result += fmt.Sprintf("Last updated: %s", m.todoRepository.GetUpdated())

	return result
}

// todoTool is a helper struct for todo tool registration
type todoTool struct {
	name        message.ToolName
	description message.ToolDescription
	arguments   []message.ToolArgument
	handler     func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)
}

func (t *todoTool) RawName() message.ToolName {
	return t.name
}

func (t *todoTool) Name() message.ToolName {
	return t.name
}

func (t *todoTool) Description() message.ToolDescription {
	return t.description
}

func (t *todoTool) Arguments() []message.ToolArgument {
	return t.arguments
}

func (t *todoTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return t.handler
}
