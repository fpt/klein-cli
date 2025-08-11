package message

import (
	"fmt"
	"time"
)

// ToolCallBatchMessage represents multiple tool calls in a single assistant turn
type ToolCallBatchMessage struct {
	ChatMessage
	calls []*ToolCallMessage
}

// NewToolCallBatch creates a batch message from individual tool calls
func NewToolCallBatch(calls []*ToolCallMessage) *ToolCallBatchMessage {
	if len(calls) == 0 {
		// create an empty placeholder to satisfy interface
		return &ToolCallBatchMessage{ChatMessage: ChatMessage{
			id:         generateMessageID(),
			typ:        MessageTypeToolCallBatch,
			content:    "batch: 0",
			timestamp:  time.Now(),
			source:     MessageSourceDefault,
			tokenUsage: TokenUsage{},
		}, calls: calls}
	}
	return &ToolCallBatchMessage{ChatMessage: ChatMessage{
		id:         generateMessageID(),
		typ:        MessageTypeToolCallBatch,
		content:    fmt.Sprintf("batch: %d tool calls", len(calls)),
		timestamp:  time.Now(),
		source:     MessageSourceDefault,
		tokenUsage: TokenUsage{},
	}, calls: calls}
}

func (b *ToolCallBatchMessage) Calls() []*ToolCallMessage { return b.calls }

// TruncatedString shows a compact representation for conversation previews
func (b *ToolCallBatchMessage) TruncatedString() string {
	return fmt.Sprintf("🔧 Used %d tools (batch)", len(b.calls))
}
