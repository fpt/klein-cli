package domain

import (
	"context"

	"github.com/fpt/klein-cli/pkg/message"
)

type State interface {
	GetMessages() []message.Message
	AddMessage(msg message.Message)
	GetLastMessage() message.Message
	Clear()
	// CleanupMandatory performs mandatory cleanup (remove images, situation messages) without compaction
	CleanupMandatory() error
	// CompactIfNeeded performs compaction only if token usage exceeds threshold.
	// Returns (true, nil) when compaction was performed, (false, nil) when not needed,
	// and (false, err) on error.
	CompactIfNeeded(ctx context.Context, llm LLM, maxTokens int, thresholdPercent float64) (bool, error)
	GetValidConversationHistory(maxMessages int) []message.Message
	RemoveMessagesBySource(source message.MessageSource) int
	// GetTotalTokenUsage returns the total token usage across all messages
	GetTotalTokenUsage() (inputTokens, outputTokens, totalTokens int)
	// Context persistence using repository
	SaveToFile() error
	LoadFromFile() error
}
