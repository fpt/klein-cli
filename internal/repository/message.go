package repository

import (
	"time"

	"github.com/fpt/klein-cli/pkg/message"
)

// MessageHistory represents a message in a serializable format
type MessageHistory struct {
	ID        string                `json:"id"`
	Type      message.MessageType   `json:"type"`
	Content   string                `json:"content"`
	Thinking  string                `json:"thinking,omitempty"`
	Images    []string              `json:"images,omitempty"`
	Timestamp time.Time             `json:"timestamp"`
	Source    message.MessageSource `json:"source"`

	// For tool messages
	ToolName string         `json:"tool_name,omitempty"`
	Args     map[string]any `json:"args,omitempty"`
	Result   string         `json:"result,omitempty"`
	Error    string         `json:"error,omitempty"`
}

// HistoryState is the serializable version of MessageState
type HistoryState struct {
	Messages []MessageHistory `json:"messages"`
	Metadata map[string]any   `json:"metadata,omitempty"`
}

// MessageHistoryRepository abstracts serialized message state persistence
type MessageHistoryRepository interface {
	Load() ([]message.Message, error)
	Save(messages []message.Message) error
	Clear() error // Delete/clear the persisted session data
}
