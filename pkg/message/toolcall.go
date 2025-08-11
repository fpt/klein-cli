package message

import (
	"fmt"
	"strings"
	"time"
)

// ToolCall represents a tool call request
type ToolCallMessage struct {
	ChatMessage
	name      ToolName
	arguments ToolArgumentValues
}

// NewToolCallMessage creates a new tool call message
func NewToolCallMessage(toolName ToolName, toolArgs ToolArgumentValues) *ToolCallMessage {
	return &ToolCallMessage{
		ChatMessage: ChatMessage{
			id:         generateMessageID(),
			typ:        MessageTypeToolCall,
			content:    fmt.Sprintf("Calling tool: %s with args: %v", toolName, toolArgs),
			timestamp:  time.Now(),
			source:     MessageSourceDefault,
			tokenUsage: TokenUsage{}, // Initialize empty token usage
		},
		name:      toolName,
		arguments: toolArgs,
	}
}

// NewToolCallMessageWithThinking creates a new tool call message with thinking content
func NewToolCallMessageWithThinking(toolName ToolName, toolArgs ToolArgumentValues, thinkingContent string) *ToolCallMessage {
	return &ToolCallMessage{
		ChatMessage: ChatMessage{
			id:         generateMessageID(),
			typ:        MessageTypeToolCall,
			content:    fmt.Sprintf("Calling tool: %s with args: %v", toolName, toolArgs),
			thinking:   thinkingContent,
			timestamp:  time.Now(),
			source:     MessageSourceDefault,
			tokenUsage: TokenUsage{}, // Initialize empty token usage
		},
		name:      toolName,
		arguments: toolArgs,
	}
}

// NewToolCallMessageWithThinkingAndSignature creates a new tool call message with thinking content and signature
func NewToolCallMessageWithThinkingAndSignature(toolName ToolName, toolArgs ToolArgumentValues, thinkingContent, signature string) *ToolCallMessage {
	// Store signature in metadata for later use when converting back to Anthropic format
	metadata := map[string]any{}
	if signature != "" {
		metadata["anthropic_thinking_signature"] = signature
	}

	return &ToolCallMessage{
		ChatMessage: ChatMessage{
			id:         generateMessageID(),
			typ:        MessageTypeToolCall,
			content:    fmt.Sprintf("Calling tool: %s with args: %v", toolName, toolArgs),
			thinking:   thinkingContent,
			timestamp:  time.Now(),
			source:     MessageSourceDefault,
			metadata:   metadata,
			tokenUsage: TokenUsage{}, // Initialize empty token usage
		},
		name:      toolName,
		arguments: toolArgs,
	}
}

// NewToolCallMessageWithID creates a new tool call message with specific ID (for session restoration)
func NewToolCallMessageWithID(id string, toolName ToolName, toolArgs ToolArgumentValues, timestamp time.Time) *ToolCallMessage {
	return &ToolCallMessage{
		ChatMessage: ChatMessage{
			id:         id,
			typ:        MessageTypeToolCall,
			content:    fmt.Sprintf("Calling tool: %s with args: %v", toolName, toolArgs),
			timestamp:  timestamp,
			source:     MessageSourceDefault,
			tokenUsage: TokenUsage{}, // Initialize empty token usage
		},
		name:      toolName,
		arguments: toolArgs,
	}
}

func (c *ToolCallMessage) ToolName() ToolName {
	return c.name
}

func (c *ToolCallMessage) ToolArguments() ToolArgumentValues {
	return c.arguments
}

// TruncatedString returns a truncated, user-friendly representation for conversation previews
func (c *ToolCallMessage) TruncatedString() string {
	return fmt.Sprintf("🔧 Used tool: %s", c.name)
}

// ToolResult represents a tool execution result
type ToolResultMessage struct {
	ChatMessage
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
}

// NewToolResultMessage creates a new tool result message
func NewToolResultMessage(callID string, result string, err string) *ToolResultMessage {
	content := result
	if err != "" {
		content = fmt.Sprintf("Error: %s", err)
	}
	return &ToolResultMessage{
		ChatMessage: ChatMessage{
			id:         callID,
			typ:        MessageTypeToolResult,
			content:    content,
			timestamp:  time.Now(),
			source:     MessageSourceDefault,
			tokenUsage: TokenUsage{}, // Initialize empty token usage
		},
		Result: result,
		Error:  err,
	}
}

// NewToolResultMessageWithImages creates a new tool result message with images
func NewToolResultMessageWithImages(callID string, result string, images []string, err string) *ToolResultMessage {
	content := result
	if err != "" {
		content = fmt.Sprintf("Error: %s", err)
	}
	return &ToolResultMessage{
		ChatMessage: ChatMessage{
			id:         callID,
			typ:        MessageTypeToolResult,
			content:    content,
			images:     images,
			timestamp:  time.Now(),
			source:     MessageSourceDefault,
			tokenUsage: TokenUsage{}, // Initialize empty token usage
		},
		Result: result,
		Error:  err,
	}
}

// TruncatedString returns a truncated, user-friendly representation for conversation previews
func (t *ToolResultMessage) TruncatedString() string {
	content := t.Result
	if t.Error != "" {
		content = fmt.Sprintf("Error: %s", t.Error)
	}

	// Show truncated tool results
	if len(content) > 100 {
		// For tool results, show first line or first 100 chars
		lines := strings.Split(content, "\n")
		if len(lines) > 0 && len(lines[0]) <= 100 {
			content = lines[0] + "..."
		} else {
			content = content[:100] + "..."
		}
	}
	return fmt.Sprintf("   ↳ %s", content)
}
