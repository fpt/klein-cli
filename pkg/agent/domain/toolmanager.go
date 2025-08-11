package domain

import (
	"context"

	"github.com/fpt/klein-cli/pkg/message"
)

// ToolAnnotator is optionally implemented by ToolManagers that provide
// dynamic annotations for their tools (e.g., cache state, status info).
// Annotations are appended to tool descriptions so the LLM can see runtime context.
type ToolAnnotator interface {
	AnnotateTools() map[message.ToolName]string
}

type ToolManager interface {
	// RegisterTool registers a new tool with the manager
	RegisterTool(name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error))

	// GetTools returns all registered tools
	GetTools() map[message.ToolName]message.Tool

	// CallTool executes a tool with the provided arguments
	CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error)
}

// ToolChoiceType represents the type of tool choice
type ToolChoiceType string

const (
	// ToolChoiceAuto allows the model to decide whether to use tools (default)
	ToolChoiceAuto ToolChoiceType = "auto"
	// ToolChoiceAny forces the model to use at least one available tool
	ToolChoiceAny ToolChoiceType = "any"
	// ToolChoiceTool forces the model to use a specific tool
	ToolChoiceTool ToolChoiceType = "tool"
	// ToolChoiceNone prevents the model from using any tools
	ToolChoiceNone ToolChoiceType = "none"
)

// ToolChoice represents the tool choice configuration
type ToolChoice struct {
	Type ToolChoiceType   `json:"type"`
	Name message.ToolName `json:"name,omitempty"` // Only used when Type is ToolChoiceTool
}

// NewToolChoiceAuto creates a tool choice that lets the model decide
func NewToolChoiceAuto() ToolChoice {
	return ToolChoice{Type: ToolChoiceAuto}
}

// NewToolChoiceAny creates a tool choice that forces the model to use any available tool
func NewToolChoiceAny() ToolChoice {
	return ToolChoice{Type: ToolChoiceAny}
}

// NewToolChoiceTool creates a tool choice that forces the model to use a specific tool
func NewToolChoiceTool(toolName message.ToolName) ToolChoice {
	return ToolChoice{Type: ToolChoiceTool, Name: toolName}
}

// NewToolChoiceNone creates a tool choice that prevents the model from using any tools
func NewToolChoiceNone() ToolChoice {
	return ToolChoice{Type: ToolChoiceNone}
}
