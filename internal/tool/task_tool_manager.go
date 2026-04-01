package tool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/repository"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

// Type alias for convenience
type TaskItem = repository.TaskItem

// TaskToolManager provides task management with dependency tracking.
type TaskToolManager struct {
	tools          map[message.ToolName]message.Tool
	taskRepository repository.TaskRepository
}

// NewTaskToolManagerWithRepository creates a TaskToolManager with an injected repository.
func NewTaskToolManagerWithRepository(repo repository.TaskRepository) *TaskToolManager {
	m := &TaskToolManager{
		tools:          make(map[message.ToolName]message.Tool),
		taskRepository: repo,
	}
	_ = repo.Load()
	m.registerTools()
	return m
}

// NewTaskToolManager creates a TaskToolManager backed by a file in the project directory.
func NewTaskToolManager(projectPath string) *TaskToolManager {
	userConfig, err := config.DefaultUserConfig()
	if err != nil {
		logger.Error("Failed to create user config directory", "error", err)
		os.Exit(1)
	}
	taskFilePath, err := userConfig.GetProjectTaskFile(projectPath)
	if err != nil {
		logger.Error("Failed to get project task file", "error", err)
		os.Exit(1)
	}
	repo := repository.NewFileTaskRepository(taskFilePath)
	return NewTaskToolManagerWithRepository(repo)
}

// NewInMemoryTaskToolManager creates a TaskToolManager backed by in-memory storage.
func NewInMemoryTaskToolManager() *TaskToolManager {
	return NewTaskToolManagerWithRepository(repository.NewInMemoryTaskRepository())
}

// domain.ToolManager interface

func (m *TaskToolManager) GetTool(name message.ToolName) (message.Tool, bool) {
	t, ok := m.tools[name]
	return t, ok
}

func (m *TaskToolManager) GetTools() map[message.ToolName]message.Tool {
	return m.tools
}

func (m *TaskToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	t, ok := m.tools[name]
	if !ok {
		return message.NewToolResultError(fmt.Sprintf("tool %s not found", name)), nil
	}
	return t.Handler()(ctx, args)
}

func (m *TaskToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	m.tools[name] = &taskTool{name: name, description: description, arguments: arguments, handler: handler}
}

// GetToolState implements domain.ToolStateProvider.
func (m *TaskToolManager) GetToolState() string {
	items := m.taskRepository.GetItems()
	var active []TaskItem
	for _, it := range items {
		if it.Status != repository.TaskStatusDeleted {
			active = append(active, it)
		}
	}
	if len(active) == 0 {
		return ""
	}
	counts := map[repository.TaskStatus]int{}
	for _, it := range active {
		counts[it.Status]++
	}
	ann := fmt.Sprintf("Task list: %d tasks", len(active))
	var parts []string
	for _, st := range []repository.TaskStatus{repository.TaskStatusInProgress, repository.TaskStatusPending, repository.TaskStatusCompleted} {
		if n := counts[st]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, st))
		}
	}
	if len(parts) > 0 {
		ann += " (" + strings.Join(parts, ", ") + ")"
	}
	return ann
}

// Compile-time check.
var _ domain.ToolStateProvider = (*TaskToolManager)(nil)

// registerTools registers all task tools.
func (m *TaskToolManager) registerTools() {
	m.RegisterTool("TaskCreate",
		"Create a new task. Returns the new task ID. Use for tracking multi-step work items with optional dependency links.",
		[]message.ToolArgument{
			{Name: "subject", Description: "Short title (one line)", Required: true, Type: "string"},
			{Name: "description", Description: "Detailed context or acceptance criteria", Required: false, Type: "string"},
			{Name: "blocked_by", Description: "Array of task IDs that must complete before this one", Required: false, Type: "array"},
		},
		m.handleCreate)

	m.RegisterTool("TaskUpdate",
		"Update an existing task's status, subject, description, or dependency links.",
		[]message.ToolArgument{
			{Name: "id", Description: "Task ID to update", Required: true, Type: "string"},
			{Name: "status", Description: "New status: pending | in_progress | completed | deleted", Required: false, Type: "string"},
			{Name: "subject", Description: "Updated title", Required: false, Type: "string"},
			{Name: "description", Description: "Updated description", Required: false, Type: "string"},
			{Name: "add_blocked_by", Description: "Task IDs to add as blockers", Required: false, Type: "array"},
			{Name: "add_blocks", Description: "Task IDs that this task now blocks", Required: false, Type: "array"},
		},
		m.handleUpdate)

	m.RegisterTool("TaskList",
		"List all active (non-deleted) tasks with their status and dependency summary.",
		[]message.ToolArgument{},
		m.handleList)

	m.RegisterTool("TaskGet",
		"Get full details of a single task including description and dependency graph.",
		[]message.ToolArgument{
			{Name: "id", Description: "Task ID", Required: true, Type: "string"},
		},
		m.handleGet)
}

// --- handlers ---

func (m *TaskToolManager) handleCreate(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	subject, _ := args["subject"].(string)
	if strings.TrimSpace(subject) == "" {
		return message.NewToolResultError("subject is required"), nil
	}
	description, _ := args["description"].(string)
	blockedBy := toStringSlice(args["blocked_by"])

	// Validate blocker IDs exist
	items := m.taskRepository.GetItems()
	existing := indexByID(items)
	for _, bid := range blockedBy {
		if _, ok := existing[bid]; !ok {
			return message.NewToolResultError(fmt.Sprintf("blocked_by task %q not found", bid)), nil
		}
	}

	now := time.Now().Format(time.RFC3339)
	id := newID()
	task := TaskItem{
		ID:          id,
		Subject:     subject,
		Description: description,
		Status:      repository.TaskStatusPending,
		BlockedBy:   blockedBy,
		Created:     now,
		Updated:     now,
	}
	items = append(items, task)

	// Add reverse links: this task is listed in Blocks of each blocker
	for i, it := range items {
		for _, bid := range blockedBy {
			if it.ID == bid {
				items[i].Blocks = appendUnique(items[i].Blocks, id)
			}
		}
	}

	m.taskRepository.SetItems(items)
	m.taskRepository.SetUpdated(now)
	if err := m.taskRepository.Save(); err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to save: %v", err)), nil
	}
	return message.NewToolResultText(fmt.Sprintf("PASS Created task #%s: %s", id, subject)), nil
}

func (m *TaskToolManager) handleUpdate(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	id, _ := args["id"].(string)
	if id == "" {
		return message.NewToolResultError("id is required"), nil
	}

	items := m.taskRepository.GetItems()
	idx := findIndex(items, id)
	if idx < 0 {
		return message.NewToolResultError(fmt.Sprintf("task %q not found", id)), nil
	}

	now := time.Now().Format(time.RFC3339)
	task := items[idx]

	if statusStr, ok := args["status"].(string); ok && statusStr != "" {
		next := repository.TaskStatus(statusStr)
		if !repository.ValidTaskStatus(next) {
			return message.NewToolResultError(fmt.Sprintf("invalid status %q; must be pending, in_progress, completed, or deleted", statusStr)), nil
		}
		if err := validateTransition(task.Status, next); err != nil {
			return message.NewToolResultError(err.Error()), nil
		}
		if next == repository.TaskStatusInProgress {
			if err := checkBlockers(items, task); err != nil {
				return message.NewToolResultError(err.Error()), nil
			}
		}
		task.Status = next
	}
	if subject, ok := args["subject"].(string); ok && subject != "" {
		task.Subject = subject
	}
	if desc, ok := args["description"].(string); ok && desc != "" {
		task.Description = desc
	}

	// add_blocked_by: add new blockers
	existing := indexByID(items)
	for _, bid := range toStringSlice(args["add_blocked_by"]) {
		if _, ok := existing[bid]; !ok {
			return message.NewToolResultError(fmt.Sprintf("add_blocked_by task %q not found", bid)), nil
		}
		task.BlockedBy = appendUnique(task.BlockedBy, bid)
		// reverse link
		for i, it := range items {
			if it.ID == bid {
				items[i].Blocks = appendUnique(items[i].Blocks, id)
			}
		}
	}

	// add_blocks: this task blocks others
	for _, fid := range toStringSlice(args["add_blocks"]) {
		if _, ok := existing[fid]; !ok {
			return message.NewToolResultError(fmt.Sprintf("add_blocks task %q not found", fid)), nil
		}
		task.Blocks = appendUnique(task.Blocks, fid)
		// reverse link
		for i, it := range items {
			if it.ID == fid {
				items[i].BlockedBy = appendUnique(items[i].BlockedBy, id)
			}
		}
	}

	task.Updated = now
	items[idx] = task
	m.taskRepository.SetItems(items)
	m.taskRepository.SetUpdated(now)
	if err := m.taskRepository.Save(); err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to save: %v", err)), nil
	}
	return message.NewToolResultText(fmt.Sprintf("PASS Updated task #%s (%s)", id, task.Status)), nil
}

func (m *TaskToolManager) handleList(_ context.Context, _ message.ToolArgumentValues) (message.ToolResult, error) {
	items := m.taskRepository.GetItems()
	var active []TaskItem
	for _, it := range items {
		if it.Status != repository.TaskStatusDeleted {
			active = append(active, it)
		}
	}
	if len(active) == 0 {
		return message.NewToolResultText("No tasks."), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Tasks (%d):\n", len(active))
	for _, it := range active {
		// Pad status field to 12 chars for alignment: "[in_progress]" is 13, "[pending]" is 9
		statusField := fmt.Sprintf("[%s]", it.Status)
		line := fmt.Sprintf("  %-13s #%s: %s", statusField, it.ID, it.Subject)
		var deps []string
		if len(it.BlockedBy) > 0 {
			deps = append(deps, "blocked by: "+strings.Join(it.BlockedBy, ", "))
		}
		if len(it.Blocks) > 0 {
			deps = append(deps, "blocks: "+strings.Join(it.Blocks, ", "))
		}
		if len(deps) > 0 {
			line += " (" + strings.Join(deps, "; ") + ")"
		}
		sb.WriteString(line + "\n")
	}
	return message.NewToolResultText(sb.String()), nil
}

func (m *TaskToolManager) handleGet(_ context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	id, _ := args["id"].(string)
	if id == "" {
		return message.NewToolResultError("id is required"), nil
	}
	items := m.taskRepository.GetItems()
	idx := findIndex(items, id)
	if idx < 0 {
		return message.NewToolResultError(fmt.Sprintf("task %q not found", id)), nil
	}
	it := items[idx]
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Task #%s\n", it.ID))
	sb.WriteString(fmt.Sprintf("Subject:     %s\n", it.Subject))
	sb.WriteString(fmt.Sprintf("Status:      %s\n", it.Status))
	if it.Description != "" {
		sb.WriteString(fmt.Sprintf("Description: %s\n", it.Description))
	}
	if len(it.BlockedBy) > 0 {
		sb.WriteString(fmt.Sprintf("Blocked by:  %s\n", strings.Join(it.BlockedBy, ", ")))
	}
	if len(it.Blocks) > 0 {
		sb.WriteString(fmt.Sprintf("Blocks:      %s\n", strings.Join(it.Blocks, ", ")))
	}
	sb.WriteString(fmt.Sprintf("Created:     %s\n", it.Created))
	sb.WriteString(fmt.Sprintf("Updated:     %s\n", it.Updated))
	return message.NewToolResultText(sb.String()), nil
}

// --- helpers ---

// validateTransition enforces the allowed status transitions:
//
//	pending    → in_progress | deleted
//	in_progress → completed | pending | deleted
//	completed  → (terminal — no further transitions)
//	deleted    → (terminal)
func validateTransition(from, to repository.TaskStatus) error {
	if from == to {
		return nil
	}
	allowed := map[repository.TaskStatus][]repository.TaskStatus{
		repository.TaskStatusPending:    {repository.TaskStatusInProgress, repository.TaskStatusDeleted},
		repository.TaskStatusInProgress: {repository.TaskStatusCompleted, repository.TaskStatusPending, repository.TaskStatusDeleted},
		repository.TaskStatusCompleted:  {},
		repository.TaskStatusDeleted:    {},
	}
	for _, ok := range allowed[from] {
		if ok == to {
			return nil
		}
	}
	return fmt.Errorf("invalid transition %s → %s", from, to)
}

// checkBlockers returns an error if any blocked_by task is not completed.
func checkBlockers(items []TaskItem, task TaskItem) error {
	byID := indexByID(items)
	for _, bid := range task.BlockedBy {
		idx, ok := byID[bid]
		if !ok {
			continue // dangling ref — skip
		}
		if items[idx].Status != repository.TaskStatusCompleted {
			return fmt.Errorf("cannot start task: blocker #%s (%s) is not completed", bid, items[idx].Status)
		}
	}
	return nil
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func indexByID(items []TaskItem) map[string]int {
	m := make(map[string]int, len(items))
	for i, it := range items {
		m[it.ID] = i
	}
	return m
}

func findIndex(items []TaskItem, id string) int {
	for i, it := range items {
		if it.ID == id {
			return i
		}
	}
	return -1
}

func appendUnique(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}

func toStringSlice(v interface{}) []string {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case []string:
		return val
	case []interface{}:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// taskTool is the message.Tool implementation for registered task tools.
type taskTool struct {
	name        message.ToolName
	description message.ToolDescription
	arguments   []message.ToolArgument
	handler     func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)
}

func (t *taskTool) RawName() message.ToolName    { return t.name }
func (t *taskTool) Name() message.ToolName        { return t.name }
func (t *taskTool) Description() message.ToolDescription { return t.description }
func (t *taskTool) Arguments() []message.ToolArgument    { return t.arguments }
func (t *taskTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return t.handler
}
