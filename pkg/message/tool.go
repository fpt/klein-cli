package message

import (
	"context"
)

type ToolName string
type ToolDescription string
type ToolArgumentValues map[string]any

// ToolResult represents the result of a tool execution
type ToolResult struct {
	Text   string   // Text content of the result
	Images []string // Base64-encoded images (if any)
	Error  string   // Error message (if any)
}

// NewToolResultText creates a tool result with only text content
func NewToolResultText(text string) ToolResult {
	return ToolResult{Text: text}
}

// NewToolResultWithImages creates a tool result with text and images
func NewToolResultWithImages(text string, images []string) ToolResult {
	return ToolResult{Text: text, Images: images}
}

// NewToolResultError creates a tool result with an error
func NewToolResultError(errorMsg string) ToolResult {
	return ToolResult{Error: errorMsg}
}

func (t ToolName) String() string {
	return string(t)
}

func (t ToolDescription) String() string {
	return string(t)
}

// Tool represents a tool definition
type Tool interface {
	RawName() ToolName
	Name() ToolName
	Description() ToolDescription
	Arguments() []ToolArgument
	Handler() func(ctx context.Context, args ToolArgumentValues) (ToolResult, error)
}

type ToolArgument struct {
	Name        ToolName
	Description ToolDescription
	Required    bool
	Type        string
	// Properties defines schema for complex types (objects, arrays)
	// For arrays: Properties["items"] = schema for array items
	// For objects: Properties contains property definitions
	Properties map[string]any `json:"properties,omitempty"`
}
