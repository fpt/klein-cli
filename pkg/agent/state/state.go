package state

import (
	"github.com/fpt/klein-cli/internal/repository"
	"github.com/fpt/klein-cli/pkg/message"
)

// Chat context with conversation history
type MessageState struct {
	Messages []message.Message `json:"-"` // Don't serialize directly
	Metadata map[string]any    `json:"-"` // Don't serialize directly

	// Repository for persistence (nil for in-memory only)
	historyRepo repository.MessageHistoryRepository

	// Token counters snapshot for telemetry (not serialized)
	tokenInput  int
	tokenOutput int
	tokenTotal  int
}

// NewMessageState creates a new message state (in-memory only)
func NewMessageState() *MessageState {
	return &MessageState{
		Messages: make([]message.Message, 0),
		Metadata: make(map[string]interface{}),
	}
}

// NewMessageStateWithRepository creates a new message state with injected serialized repository
func NewMessageStateWithRepository(serializedRepository repository.MessageHistoryRepository) *MessageState {
	return &MessageState{
		Messages:    make([]message.Message, 0),
		Metadata:    make(map[string]interface{}),
		historyRepo: serializedRepository,
	}
}

func (c *MessageState) GetMessages() []message.Message {
	return c.Messages
}

// AddMessage adds a message to the context
func (c *MessageState) AddMessage(msg message.Message) {
	c.Messages = append(c.Messages, msg)
}

// GetLastMessage returns the last message in the context
func (c *MessageState) GetLastMessage() message.Message {
	if len(c.Messages) == 0 {
		return nil
	}
	return c.Messages[len(c.Messages)-1]
}

// Clear clears all messages from the context and deletes persisted session data
func (c *MessageState) Clear() {
	c.Messages = make([]message.Message, 0)

	// Also clear persisted data if repository is available
	if c.historyRepo != nil {
		_ = c.historyRepo.Clear() // Ignore error - clearing in-memory is more important
	}
}

// ResetTokenCounters clears the internal token counters snapshot
func (c *MessageState) ResetTokenCounters() {
	c.tokenInput, c.tokenOutput, c.tokenTotal = 0, 0, 0
}

// RecalculateTokenCountersFromMessages recomputes counters by summing input+output across messages
func (c *MessageState) RecalculateTokenCountersFromMessages() {
	in, out, _ := c.GetTotalTokenUsage()
	c.tokenInput = in
	c.tokenOutput = out
	c.tokenTotal = in + out
}

// TokenCountersSnapshot returns the last computed counters (input, output, total)
func (c *MessageState) TokenCountersSnapshot() (int, int, int) {
	return c.tokenInput, c.tokenOutput, c.tokenTotal
}

// RemoveMessagesBySource removes all messages with the specified source
// Returns the number of messages removed
func (c *MessageState) RemoveMessagesBySource(source message.MessageSource) int {
	filteredMessages := make([]message.Message, 0, len(c.Messages))
	removedCount := 0

	for _, msg := range c.Messages {
		if msg.Source() == source {
			removedCount++
			continue // Skip messages with the specified source
		}
		filteredMessages = append(filteredMessages, msg)
	}

	if removedCount > 0 {
		c.Messages = filteredMessages
	}

	return removedCount
}

// GetValidConversationHistory returns recent messages while ensuring tool call/result pairs are kept together
// This prevents API validation errors when including conversation history in requests
func (c *MessageState) GetValidConversationHistory(maxMessages int) []message.Message {
	if len(c.Messages) == 0 {
		return nil
	}

	// Simple approach: work backwards and collect messages, but skip orphaned tool calls/results
	var validMessages []message.Message

	// First pass: identify all complete tool call/result pairs
	toolPairs := make(map[string]bool) // Maps tool call IDs to whether they have complete pairs
	for i := 0; i < len(c.Messages); i++ {
		if c.Messages[i].Type() == message.MessageTypeToolCall {
			toolID := c.Messages[i].ID()
			// Look ahead for the corresponding result
			for j := i + 1; j < len(c.Messages); j++ {
				if c.Messages[j].Type() == message.MessageTypeToolResult && c.Messages[j].ID() == toolID {
					toolPairs[toolID] = true
					break
				}
			}
		}
	}

	// Second pass: collect messages from the end, including only complete tool pairs
	for i := len(c.Messages) - 1; i >= 0 && len(validMessages) < maxMessages; i-- {
		msg := c.Messages[i]

		switch msg.Type() {
		case message.MessageTypeToolCall:
			// Only include if it has a complete pair
			if toolPairs[msg.ID()] {
				validMessages = append([]message.Message{msg}, validMessages...)
			}
		case message.MessageTypeToolResult:
			// Only include if its corresponding call has a complete pair
			if toolPairs[msg.ID()] {
				validMessages = append([]message.Message{msg}, validMessages...)
			}
		case message.MessageTypeUser, message.MessageTypeAssistant, message.MessageTypeSystem:
			// Regular messages are always safe to include
			validMessages = append([]message.Message{msg}, validMessages...)
		}
	}

	return validMessages
}

// SaveToFile saves the message state using the repository
func (c *MessageState) SaveToFile() error {
	if c.historyRepo == nil {
		return nil // No repository configured, skip save
	}
	return c.historyRepo.Save(c.Messages)
}

// LoadFromFile loads the message state using the repository
func (c *MessageState) LoadFromFile() error {
	if c.historyRepo == nil {
		return nil // No repository configured, skip load
	}

	messages, err := c.historyRepo.Load()
	if err != nil {
		return err
	}

	c.Messages = messages
	if c.Metadata == nil {
		c.Metadata = make(map[string]interface{})
	}
	return nil
}
