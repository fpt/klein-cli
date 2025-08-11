package tool

import (
	"context"
	"fmt"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

// CompositeToolManager combines multiple tool managers into one
type CompositeToolManager struct {
	managers   []domain.ToolManager
	annotators []domain.ToolAnnotator
	toolsMap   map[message.ToolName]message.Tool
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
		// Collect annotators via type assertion.
		if ann, ok := manager.(domain.ToolAnnotator); ok {
			composite.annotators = append(composite.annotators, ann)
		}
	}

	return composite
}

// GetTool returns a tool by name from any of the managed tool managers
func (c *CompositeToolManager) GetTool(name message.ToolName) (message.Tool, bool) {
	tool, exists := c.toolsMap[name]
	return tool, exists
}

// GetTools returns all tools, applying dynamic annotations from any ToolAnnotator managers.
func (c *CompositeToolManager) GetTools() map[message.ToolName]message.Tool {
	if len(c.annotators) == 0 {
		return c.toolsMap
	}

	// Collect all annotations.
	annotations := make(map[message.ToolName]string)
	for _, ann := range c.annotators {
		for name, text := range ann.AnnotateTools() {
			if text != "" {
				annotations[name] = text
			}
		}
	}
	if len(annotations) == 0 {
		return c.toolsMap
	}

	// Build a new map with annotated descriptions where applicable.
	result := make(map[message.ToolName]message.Tool, len(c.toolsMap))
	for name, tool := range c.toolsMap {
		if ann, ok := annotations[name]; ok {
			result[name] = &annotatedTool{
				Tool: tool,
				desc: message.ToolDescription(fmt.Sprintf("%s (%s)", tool.Description(), ann)),
			}
		} else {
			result[name] = tool
		}
	}
	return result
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

// annotatedTool wraps a Tool with a dynamically enhanced description.
type annotatedTool struct {
	message.Tool
	desc message.ToolDescription
}

func (a *annotatedTool) Description() message.ToolDescription {
	return a.desc
}
