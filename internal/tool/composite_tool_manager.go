package tool

import (
	"context"
	"fmt"
	"strings"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

// CompositeToolManager combines multiple tool managers into one.
// It also implements domain.ToolStateProvider by aggregating state from child managers.
type CompositeToolManager struct {
	managers       []domain.ToolManager
	stateProviders []domain.ToolStateProvider
	toolsMap       map[message.ToolName]message.Tool
}

// NewCompositeToolManager creates a new composite tool manager from multiple managers
func NewCompositeToolManager(managers ...domain.ToolManager) *CompositeToolManager {
	composite := &CompositeToolManager{
		managers: managers,
		toolsMap: make(map[message.ToolName]message.Tool),
	}

	// Build unified tools map from all managers
	for _, manager := range managers {
		tools := manager.GetTools()
		for _, tool := range tools {
			composite.toolsMap[tool.Name()] = tool
		}
		// Collect state providers via type assertion.
		if sp, ok := manager.(domain.ToolStateProvider); ok {
			composite.stateProviders = append(composite.stateProviders, sp)
		}
	}

	return composite
}

// GetToolState implements domain.ToolStateProvider.
// Aggregates state strings from all child managers that implement ToolStateProvider.
func (c *CompositeToolManager) GetToolState() string {
	var parts []string
	for _, sp := range c.stateProviders {
		if s := sp.GetToolState(); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n")
}

// GetTool returns a tool by name from any of the managed tool managers
func (c *CompositeToolManager) GetTool(name message.ToolName) (message.Tool, bool) {
	tool, exists := c.toolsMap[name]
	return tool, exists
}

// GetTools returns all tools with their static descriptions.
// Dynamic runtime state (cache entries, todo counts) is exposed via GetToolState()
// and injected into situation messages to keep tool descriptions stable for prompt caching.
func (c *CompositeToolManager) GetTools() map[message.ToolName]message.Tool {
	return c.toolsMap
}

// CallTool executes a tool from any of the managed tool managers
func (c *CompositeToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	tool, exists := c.toolsMap[name]
	if !exists {
		return message.NewToolResultError(fmt.Sprintf("tool %s not found", name)), nil
	}

	handler := tool.Handler()
	return handler(ctx, args)
}

// RegisterTool is not supported on composite managers since tools should be registered on the underlying managers
func (c *CompositeToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, args []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	panic("RegisterTool not supported on CompositeToolManager - register on underlying managers instead")
}

