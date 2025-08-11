package infra

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"

	"github.com/fpt/klein-cli/internal/repository"
	"github.com/fpt/klein-cli/pkg/message"
)

// MessageHistoryRepository represents file-persisted serialized message repository
type MessageHistoryRepository struct {
	filePath string
}

// NewMessageHistoryRepository creates a new file-based serialized message repository
func NewMessageHistoryRepository(filePath string) *MessageHistoryRepository {
	return &MessageHistoryRepository{
		filePath: filePath,
	}
}

// Load implements repository.MessageHistoryRepository
func (fr *MessageHistoryRepository) Load() ([]message.Message, error) {
	if fr.filePath == "" {
		return nil, fmt.Errorf("no file path specified")
	}

	data, err := os.ReadFile(fr.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// File doesn't exist yet, return empty messages
			return make([]message.Message, 0), nil
		}
		return nil, fmt.Errorf("failed to read state file %s: %w", fr.filePath, err)
	}

	var serializableState repository.HistoryState
	if err := json.Unmarshal(data, &serializableState); err != nil {
		return nil, fmt.Errorf("failed to deserialize state from %s: %w", fr.filePath, err)
	}

	// Convert serializable messages back to message.Message
	messages := make([]message.Message, len(serializableState.Messages))
	for i, serializableMsg := range serializableState.Messages {
		messages[i] = serializableToMessage(serializableMsg)
	}

	return messages, nil
}

// Save implements repository.MessageHistoryRepository
func (fr *MessageHistoryRepository) Save(messages []message.Message) error {
	if fr.filePath == "" {
		return fmt.Errorf("no file path specified")
	}

	// Convert messages to serializable format
	serializableMessages := make([]repository.MessageHistory, len(messages))
	for i, msg := range messages {
		serializableMessages[i] = messageToSerializable(msg)
	}

	state := repository.HistoryState{
		Messages: serializableMessages,
		Metadata: make(map[string]any), // We don't store metadata in this implementation
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize state: %w", err)
	}

	// Ensure directory exists
	dir := filepath.Dir(fr.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	if err := os.WriteFile(fr.filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file %s: %w", fr.filePath, err)
	}

	return nil
}

// Clear implements repository.MessageHistoryRepository
func (fr *MessageHistoryRepository) Clear() error {
	if fr.filePath == "" {
		return fmt.Errorf("no file path specified")
	}

	if err := os.Remove(fr.filePath); err != nil {
		if os.IsNotExist(err) {
			// File doesn't exist, nothing to clear
			return nil
		}
		return fmt.Errorf("failed to delete state file %s: %w", fr.filePath, err)
	}

	return nil
}

// messageToSerializable converts a Message interface to repository.MessageHistory
func messageToSerializable(msg message.Message) repository.MessageHistory {
	if msg == nil {
		return repository.MessageHistory{}
	}

	serializable := repository.MessageHistory{
		ID:        msg.ID(),
		Type:      msg.Type(),
		Content:   msg.Content(),
		Thinking:  msg.Thinking(),
		Images:    msg.Images(),
		Timestamp: msg.Timestamp(),
		Source:    msg.Source(),
	}

	// Handle tool-specific fields if it's a tool call or result message
	switch msg.Type() {
	case message.MessageTypeToolCall:
		// Try to cast to ToolCallMessage to extract tool info
		if toolCall, ok := msg.(*message.ToolCallMessage); ok {
			serializable.ToolName = string(toolCall.ToolName())
			args := make(map[string]any)
			maps.Copy(args, toolCall.ToolArguments())
			serializable.Args = args
		}
	case message.MessageTypeToolResult:
		// Try to cast to ToolResultMessage to extract result/error info
		if toolResult, ok := msg.(*message.ToolResultMessage); ok {
			serializable.Result = toolResult.Result
			serializable.Error = toolResult.Error
		} else {
			// Fallback: store content as result
			serializable.Result = msg.Content()
		}
	}

	return serializable
}

// serializableToMessage converts repository.MessageHistory back to Message interface
func serializableToMessage(s repository.MessageHistory) message.Message {
	switch s.Type {
	case message.MessageTypeToolCall:
		// Create tool call message with proper types and original ID
		toolName := message.ToolName(s.ToolName)
		args := make(message.ToolArgumentValues)
		maps.Copy(args, s.Args)
		return message.NewToolCallMessageWithID(s.ID, toolName, args, s.Timestamp)
	case message.MessageTypeToolResult:
		// Create tool result message
		if s.Error != "" {
			return message.NewToolResultMessage(s.ID, "", s.Error)
		}
		return message.NewToolResultMessage(s.ID, s.Result, "")
	case message.MessageTypeSystem:
		// Handle system messages with different sources
		switch s.Source {
		case message.MessageSourceSituation:
			return message.NewSituationSystemMessage(s.Content)
		case message.MessageSourceSummary:
			return message.NewSummarySystemMessage(s.Content)
		default:
			return message.NewSystemMessage(s.Content)
		}
	default:
		// Create regular chat message - we'll lose some metadata like custom ID and timestamp
		// but this is better than crashing
		if s.Thinking != "" {
			return message.NewChatMessageWithThinking(s.Type, s.Content, s.Thinking)
		}
		if len(s.Images) > 0 {
			return message.NewChatMessageWithImages(s.Type, s.Content, s.Images)
		}
		return message.NewChatMessage(s.Type, s.Content)
	}
}
